package dbinspect_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dbinspect"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func TestInspectCounts(t *testing.T) {
	ctx := context.Background()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	for _, id := range []string{"a1", "a2"} {
		if err := sqliteadapter.NewArticleRepository(db).Save(ctx, article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqliteadapter.NewOutboxRepository(db).Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', ?, ?, 'open')`, now, now); err != nil {
		t.Fatal(err)
	}
	got, err := dbinspect.Inspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	want := dbinspect.Counts{Articles: 2, OutboxPending: 1, OpenClusters: 1}
	if got != want {
		t.Fatalf("Inspect() = %+v, want %+v", got, want)
	}
}
