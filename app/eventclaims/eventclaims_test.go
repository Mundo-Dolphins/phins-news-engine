package eventclaims_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventclaims"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type env struct {
	t     *testing.T
	db    *sql.DB
	store eventclaims.Store
}

func newEnv(t *testing.T) env {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return env{t: t, db: db, store: sqliteadapter.NewEventClaimRepository(db)}
}

// seed stores a claim created at createdAt with one outbox row per status
// (destinations telegram then bluesky, last_error "boom <status>").
func (e env) seed(key, state, articleID string, createdAt time.Time, statuses ...string) {
	e.t.Helper()
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(e.t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	if err := sqliteadapter.NewArticleRepository(e.db).Save(ctx, article.Article{
		ID: articleID, OriginalURL: "https://example.com/" + articleID, CanonicalURL: "https://example.com/" + articleID,
		Source: src, FetchedAt: createdAt, PublishedAt: createdAt, Status: article.StatusPublished,
	}); err != nil {
		e.t.Fatalf("seed article: %v", err)
	}
	if ok, err := e.store.TryClaim(ctx, "injury", key, articleID, json.RawMessage(`{"week":3}`), createdAt); err != nil || !ok {
		e.t.Fatalf("TryClaim = %v, %v", ok, err)
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE event_claims SET state = ? WHERE claim_key = ?`, state, key); err != nil {
		e.t.Fatalf("set state: %v", err)
	}
	destinations := []string{"telegram", "bluesky"}
	for i, status := range statuses {
		if _, err := e.db.ExecContext(ctx, `
			INSERT INTO publication_outbox (article_id, destination, payload, status, available_at, attempt_count, last_error, expires_at)
			VALUES (?, ?, '{}', ?, ?, 6, ?, ?)`,
			articleID, destinations[i], status, createdAt, "boom "+status, createdAt); err != nil {
			e.t.Fatalf("seed outbox: %v", err)
		}
	}
}

func TestListFiltersSince(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	e.seed("old", "claimed", "a1", now.Add(-48*time.Hour))
	e.seed("new", "claimed", "a2", now.Add(-time.Hour))

	views, err := eventclaims.List(context.Background(), e.store, out.EventClaimFilter{}, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Key != "new" {
		t.Fatalf("views = %+v, want only the 1h-old claim", views)
	}
	all, err := eventclaims.List(context.Background(), e.store, out.EventClaimFilter{}, time.Time{})
	if err != nil || len(all) != 2 {
		t.Fatalf("zero since: %d views, %v; want 2", len(all), err)
	}
}

func TestListAttachesOutboxAndLastError(t *testing.T) {
	e := newEnv(t)
	e.seed("k1", "failed", "a1", time.Now(), "delivered", "dead")
	e.seed("k2", "claimed", "a2", time.Now())

	views, err := eventclaims.List(context.Background(), e.store, out.EventClaimFilter{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]eventclaims.View{}
	for _, v := range views {
		byKey[v.Key] = v
	}
	if v := byKey["k1"]; len(v.Outbox) != 2 || v.Outbox[1].Status != "dead" || v.LastError != "boom dead" || v.State != out.EventClaimFailed {
		t.Errorf("k1 = %+v", v)
	}
	if v := byKey["k2"]; len(v.Outbox) != 0 || v.LastError != "" {
		t.Errorf("k2 = %+v", v)
	}
}

func TestShowNotFound(t *testing.T) {
	e := newEnv(t)
	_, err := eventclaims.Show(context.Background(), e.store, "injury", "nope")
	if !errors.Is(err, out.ErrEventClaimNotFound) {
		t.Fatalf("err = %v, want ErrEventClaimNotFound", err)
	}
	e.seed("k1", "failed", "a1", time.Now(), "dead")
	v, err := eventclaims.Show(context.Background(), e.store, "injury", "k1")
	if err != nil || v.Handler != "injury" || v.LastError != "boom dead" || len(v.Outbox) != 1 {
		t.Fatalf("show = %+v, %v", v, err)
	}
}

func TestCheckRetry(t *testing.T) {
	tests := []struct {
		name    string
		state   string
		rows    []string
		missing bool
		want    error
		dead    int
	}{
		{name: "not found", missing: true, want: out.ErrEventClaimNotFound},
		{name: "not failed", state: "claimed", rows: []string{"dead"}, want: out.ErrEventClaimNotFailed},
		{name: "in flight", state: "failed", rows: []string{"dead", "pending"}, want: out.ErrEventClaimInFlight},
		{name: "nothing to retry", state: "failed", rows: []string{"delivered"}, want: out.ErrEventClaimNothingToRetry},
		{name: "ok", state: "failed", rows: []string{"dead", "dead"}, dead: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if !tt.missing {
				e.seed("k1", tt.state, "a1", time.Now(), tt.rows...)
			}
			n, err := eventclaims.CheckRetry(context.Background(), e.store, "injury", "k1")
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Fatalf("err = %v, want %v", err, tt.want)
				}
				return
			}
			if err != nil || n != tt.dead {
				t.Fatalf("CheckRetry = %d, %v; want %d", n, err, tt.dead)
			}
		})
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	got, err := eventclaims.ParseSince("24h", now)
	if err != nil || !got.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("24h = %v, %v", got, err)
	}
	got, err = eventclaims.ParseSince("2026-11-01", now)
	if err != nil || !got.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v, %v", got, err)
	}
	if _, err := eventclaims.ParseSince("yesterday", now); err == nil {
		t.Error("want error")
	}
}
