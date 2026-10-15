package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

func TestSourceRepository_SaveAndList(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewSourceRepository(db)
	ctx := context.Background()

	s := source.Source{ID: mustID(t, source.TypeRSS, "isotopes-rss"), Name: "Springfield Isotopes RSS", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com/feed.xml"}
	if err := repo.Save(ctx, s); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != s.ID || got[0].Type != s.Type {
		t.Errorf("List() = %+v, want [%+v]", got, s)
	}
}

func TestSourceRepository_SaveUpsertsOnConflict(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewSourceRepository(db)
	ctx := context.Background()

	s := source.Source{ID: mustID(t, source.TypeRSS, "isotopes-rss"), Name: "Old Name", Type: source.TypeRSS}
	if err := repo.Save(ctx, s); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	s.Name = "New Name"
	if err := repo.Save(ctx, s); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	got, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() len = %d, want 1 (upsert, not duplicate row)", len(got))
	}
	if got[0].Name != "New Name" {
		t.Errorf("Name = %q, want %q", got[0].Name, "New Name")
	}
}

func TestSourceRepository_SavePreservesFirstSeenAtAcrossUpdates(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewSourceRepository(db)
	ctx := context.Background()

	id, err := source.ParseID("rss:example")
	if err != nil {
		t.Fatalf("ParseID() error = %v", err)
	}
	firstSeen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := repo.Save(ctx, source.Source{ID: id, Name: "Example", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com", FirstSeenAt: firstSeen}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// A later Save (e.g. a config resync) with a zero FirstSeenAt must
	// not clobber the originally recorded value.
	if err := repo.Save(ctx, source.Source{ID: id, Name: "Example Updated", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"}); err != nil {
		t.Fatalf("Save() (update) error = %v", err)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List() returned %d sources, want 1", len(list))
	}
	got := list[0]
	if got.Name != "Example Updated" {
		t.Errorf("Name = %q, want %q", got.Name, "Example Updated")
	}
	if !got.FirstSeenAt.Equal(firstSeen) {
		t.Errorf("FirstSeenAt = %v, want %v (must be preserved, not overwritten)", got.FirstSeenAt, firstSeen)
	}
}

func TestSourceRepository_SaveDefaultsFirstSeenAtWhenUnset(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewSourceRepository(db)
	ctx := context.Background()

	id, err := source.ParseID("rss:newexample")
	if err != nil {
		t.Fatalf("ParseID() error = %v", err)
	}
	before := time.Now().UTC()
	if err := repo.Save(ctx, source.Source{ID: id, Name: "New", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	after := time.Now().UTC()

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List() returned %d sources, want 1", len(list))
	}
	got := list[0].FirstSeenAt
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("FirstSeenAt = %v, want within [%v, %v]", got, before, after)
	}
}
