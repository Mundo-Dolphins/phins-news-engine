package seenitems

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestRunSeenItemsBackfill_BackfillsOneRowPerIdentityKeepingMostRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backfill.db")
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insert := func(id, externalID, canonicalURL, title, status string, fetchedAt time.Time) {
		_, err := db.ExecContext(context.Background(), `
			INSERT INTO articles (id, external_id, original_url, canonical_url, source_id, source_type, title, fetched_at, status)
			VALUES (?, ?, ?, ?, 'feed-a', 'rss', ?, ?, ?)
		`, id, externalID, canonicalURL, canonicalURL, title, fetchedAt, status)
		if err != nil {
			t.Fatalf("insert article %s: %v", id, err)
		}
	}
	// Same identity (external_id "guid-1"), two historical rows — the
	// most-recently-fetched one must win.
	insert("article-old", "guid-1", "https://example.com/a", "Old title", "rejected_not_related", now.Add(-48*time.Hour))
	insert("article-new", "guid-1", "https://example.com/a-updated", "New title", "published", now.Add(-1*time.Hour))
	// A distinct identity with no external_id — falls back to canonical_url.
	insert("article-web", "", "https://example.com/website-story", "Website story", "published", now.Add(-2*time.Hour))

	result, err := RunSeenItemsBackfill(context.Background(), db, now)
	if err != nil {
		t.Fatalf("RunSeenItemsBackfill() error = %v", err)
	}
	if result.Candidates != 2 || result.Inserted != 2 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want Candidates=2 Inserted=2 Skipped=0", result)
	}

	seenItems := sqliteadapter.NewSeenItemsRepository(db)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/a-updated")
	claim, err := seenItems.Claim(context.Background(), identity, "should-not-be-used", "irrelevant-hash", "https://example.com/a-updated", now, 10*time.Minute)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claim.Status != out.SeenClaimRevised && claim.Status != out.SeenClaimSeen {
		t.Fatalf("claim.Status = %v, want Revised or Seen (an existing backfilled row must be found)", claim.Status)
	}
	if claim.ArticleID != "article-new" {
		t.Fatalf("claim.ArticleID = %s, want article-new (most-recently-fetched row must win the backfill)", claim.ArticleID)
	}

	// Re-running is a no-op (idempotent).
	result2, err := RunSeenItemsBackfill(context.Background(), db, now)
	if err != nil {
		t.Fatalf("second RunSeenItemsBackfill() error = %v", err)
	}
	if result2.Inserted != 0 || result2.Skipped != 2 {
		t.Fatalf("result2 = %+v, want Inserted=0 Skipped=2 (already-backfilled identities must be no-ops)", result2)
	}
}
