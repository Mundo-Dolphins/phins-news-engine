package sqliteknowledgecache_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteknowledgecache"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
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

var (
	now  = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	key  = out.SearchKey{Provider: "wikidata", Language: "en", Limit: 5, Query: "patrick ostrander"}
	ekey = out.EntityKey{Provider: "wikidata", Language: "en", ID: "Q1"}
)

func TestCache_SearchRoundTrip(t *testing.T) {
	c := sqliteknowledgecache.NewCache(newTestDB(t))
	ctx := context.Background()
	want := out.CachedSearch{
		Candidates: []knowledge.Candidate{{ID: "Q1", Label: "Patrick Ostrander", Aliases: []string{"Ostrander"}}},
		ExpiresAt:  now.Add(time.Hour),
	}
	if err := c.PutSearch(ctx, key, want, now); err != nil {
		t.Fatalf("PutSearch() error = %v", err)
	}
	got, ok, err := c.GetSearch(ctx, key)
	if err != nil || !ok {
		t.Fatalf("GetSearch() = %v, %v, %v", got, ok, err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].ID != "Q1" || got.Negative || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("GetSearch() = %+v, want round-trip of %+v", got, want)
	}
}

func TestCache_SearchMissAndNegativeAndOverwrite(t *testing.T) {
	c := sqliteknowledgecache.NewCache(newTestDB(t))
	ctx := context.Background()
	if _, ok, err := c.GetSearch(ctx, key); err != nil || ok {
		t.Fatalf("GetSearch(unknown) ok=%v err=%v, want miss", ok, err)
	}
	if err := c.PutSearch(ctx, key, out.CachedSearch{Negative: true, ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := c.GetSearch(ctx, key)
	if !ok || !got.Negative || len(got.Candidates) != 0 {
		t.Fatalf("negative entry = %+v ok=%v", got, ok)
	}
	// A different limit is a different key.
	other := key
	other.Limit = 3
	if _, ok, _ := c.GetSearch(ctx, other); ok {
		t.Fatal("different limit must not share a cache entry")
	}
	// Overwrite replaces.
	pos := out.CachedSearch{Candidates: []knowledge.Candidate{{ID: "Q9"}}, ExpiresAt: now.Add(2 * time.Hour)}
	if err := c.PutSearch(ctx, key, pos, now); err != nil {
		t.Fatal(err)
	}
	got, _, _ = c.GetSearch(ctx, key)
	if got.Negative || len(got.Candidates) != 1 || got.Candidates[0].ID != "Q9" {
		t.Fatalf("after overwrite = %+v", got)
	}
}

func TestCache_ExpiredRowsAreStillReturnedWithTheirExpiry(t *testing.T) {
	c := sqliteknowledgecache.NewCache(newTestDB(t))
	ctx := context.Background()
	past := now.Add(-time.Hour)
	if err := c.PutSearch(ctx, key, out.CachedSearch{Candidates: []knowledge.Candidate{{ID: "Q1"}}, ExpiresAt: past}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.GetSearch(ctx, key)
	if err != nil || !ok || !got.ExpiresAt.Equal(past) {
		t.Fatalf("GetSearch() = %+v ok=%v err=%v, want the expired row with ExpiresAt=%v", got, ok, err, past)
	}
}

func TestCache_EntityRoundTripIncludingStatementsAndNegative(t *testing.T) {
	c := sqliteknowledgecache.NewCache(newTestDB(t))
	ctx := context.Background()
	from := time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)
	ent := knowledge.Entity{
		ID: "Q1", Label: "Patrick Ostrander",
		Statements: []knowledge.Statement{{
			Property: "P54", Rank: knowledge.RankNormal, ValidFrom: &from,
			Value: knowledge.Value{Kind: knowledge.KindEntity, EntityID: "Q910000008"},
		}},
	}
	if err := c.PutEntity(ctx, ekey, out.CachedEntity{Entity: ent, ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.GetEntity(ctx, ekey)
	if err != nil || !ok || got.Negative {
		t.Fatalf("GetEntity() = %+v ok=%v err=%v", got, ok, err)
	}
	st := got.Entity.Statements[0]
	if st.Value.EntityID != "Q910000008" || st.ValidFrom == nil || !st.ValidFrom.Equal(from) {
		t.Fatalf("statement not round-tripped: %+v", st)
	}

	neg := out.EntityKey{Provider: "wikidata", Language: "en", ID: "Q404"}
	if err := c.PutEntity(ctx, neg, out.CachedEntity{Negative: true, ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	got, ok, _ = c.GetEntity(ctx, neg)
	if !ok || !got.Negative {
		t.Fatalf("negative entity = %+v ok=%v", got, ok)
	}
	// Language is part of the key.
	if _, ok, _ := c.GetEntity(ctx, out.EntityKey{Provider: "wikidata", Language: "es", ID: "Q1"}); ok {
		t.Fatal("different language must not share an entity entry")
	}
}

func TestCache_PurgeDeletesOnlyRowsExpiredBeforeCutoff(t *testing.T) {
	c := sqliteknowledgecache.NewCache(newTestDB(t))
	ctx := context.Background()
	old := out.CachedSearch{Negative: true, ExpiresAt: now.Add(-48 * time.Hour)}
	recent := out.CachedSearch{Negative: true, ExpiresAt: now.Add(-time.Hour)}
	// Fractional seconds exercise the fixed-width timestamp comparison.
	fractional := out.CachedSearch{Negative: true, ExpiresAt: now.Add(-48*time.Hour + 500*time.Millisecond)}
	for i, v := range []out.CachedSearch{old, recent, fractional} {
		k := key
		k.Query = []string{"old", "recent", "fractional"}[i]
		if err := c.PutSearch(ctx, k, v, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PutEntity(ctx, ekey, out.CachedEntity{Negative: true, ExpiresAt: now.Add(-72 * time.Hour)}, now); err != nil {
		t.Fatal(err)
	}

	n, err := c.Purge(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if n != 3 {
		t.Fatalf("Purge() = %d rows, want 3 (old, fractional, entity)", n)
	}
	k := key
	k.Query = "recent"
	if _, ok, _ := c.GetSearch(ctx, k); !ok {
		t.Fatal("recent row must survive Purge")
	}
}
