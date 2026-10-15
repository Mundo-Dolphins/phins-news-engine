package knowledgecache_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteknowledgecache"
	"github.com/Mundo-Dolphins/phins-news-engine/app/knowledgecache"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/wikidata"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := infrasqlite.Open(t.TempDir()+"/test.db", 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

func TestIntegration_RepeatedSearchIsOneHTTPRequestAndStaleOnOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps ~2s on retry backoff")
	}
	var hits atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"search":[{"id":"Q1","label":"Patrick Ostrander"}]}`))
	}))
	defer srv.Close()

	src, err := wikidata.New(wikidata.Config{BaseURL: srv.URL, Language: "en", UserAgent: "t/1 (https://example.com)", Timeout: time.Second, MaxCandidates: 5, AllowPrivateHosts: true})
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	c := knowledgecache.New(src, sqliteknowledgecache.NewCache(newTestDB(t)), clock, knowledgecache.Config{
		Provider: wikidata.ProviderName, Language: "en", PositiveTTL: time.Hour, NegativeTTL: time.Minute, AllowStaleOnError: true,
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if got, err := c.Search(ctx, "Patrick Ostrander", 5); err != nil || len(got) != 1 {
			t.Fatalf("Search() = %v, %v", got, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1", hits.Load())
	}

	clock.t = clock.t.Add(2 * time.Hour)
	fail.Store(true)
	if got, err := c.Search(ctx, "Patrick Ostrander", 5); err != nil || len(got) != 1 {
		t.Fatalf("stale Search() = %v, %v, want stale hit during outage", got, err)
	}
}
