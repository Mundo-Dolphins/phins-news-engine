package sqlitewebsearchcache_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitewebsearchcache"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
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

func TestCache_SetThenGet_ReturnsStoredResults(t *testing.T) {
	db := newTestDB(t)
	c := sqlitewebsearchcache.NewCache(db)
	ctx := context.Background()

	results := []weblookup.SearchResult{{Title: "Isotopes Add CB Jai'Rene' Mossbank", Domain: "isotopes.example.com", URL: "https://isotopes.example.com/news/1", Trusted: true}}
	if err := c.Set(ctx, "key1", "test-provider", `"Jai'Rene' Mossbank" Isotopes`, results, time.Hour); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, ok, err := c.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if len(got) != 1 || got[0].Domain != "isotopes.example.com" {
		t.Fatalf("Get() = %+v, want the stored result round-tripped", got)
	}
}

func TestCache_Get_MissesOnUnknownKey(t *testing.T) {
	db := newTestDB(t)
	c := sqlitewebsearchcache.NewCache(db)

	_, ok, err := c.Get(context.Background(), "unknown-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ok {
		t.Fatal("Get() ok = true, want false for an unknown key")
	}
}

func TestCache_Get_MissesOnExpiredEntry(t *testing.T) {
	db := newTestDB(t)
	c := sqlitewebsearchcache.NewCache(db)
	ctx := context.Background()

	if err := c.Set(ctx, "expired-key", "test-provider", "query", []weblookup.SearchResult{{Domain: "league.example.com"}}, -time.Hour); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	_, ok, err := c.Get(ctx, "expired-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ok {
		t.Fatal("Get() ok = true, want false for an expired entry")
	}
}
