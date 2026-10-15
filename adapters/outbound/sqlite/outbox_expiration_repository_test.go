package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type expirationTestClock struct{ now time.Time }

func (c expirationTestClock) Now() time.Time { return c.now }

func TestOutboxExpirationRepository_SelectsOnlyOldClaimableRows(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	repo := sqliteadapter.NewOutboxExpirationRepository(db)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	statuses := map[string]string{"pending": "pending", "retryable": "retryable", "leased": "leased", "sent": "sent_unconfirmed", "quarantine": "quarantined", "delivered": "delivered", "dead": "dead"}
	for id, status := range statuses {
		mustSaveArticleWithPublishedAt(t, articles, id, now.Add(-48*time.Hour))
		if err := outbox.Enqueue(context.Background(), id, "{}", now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE publication_outbox SET status = ? WHERE article_id = ?`, status, id); err != nil {
			t.Fatal(err)
		}
	}
	mustSaveArticleWithPublishedAt(t, articles, "recent", now.Add(-time.Hour))
	if err := outbox.Enqueue(context.Background(), "recent", "{}", now); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListExpirationCandidates(context.Background(), out.OutboxExpirationFilter{PublishedBefore: now.Add(-24 * time.Hour), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, candidate := range candidates {
		got[candidate.ArticleID] = true
	}
	if len(candidates) != 2 || !got["pending"] || !got["retryable"] {
		t.Fatalf("candidates = %+v, want pending and retryable only", candidates)
	}
}

func TestOutboxExpirationRepository_ExpireGuardIsIdempotentAndProtectsStates(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	repo := sqliteadapter.NewOutboxExpirationRepository(db)
	now := time.Now()
	mustSaveArticleWithPublishedAt(t, articles, "a1", now.Add(-48*time.Hour))
	if err := outbox.Enqueue(context.Background(), "a1", "{}", now); err != nil {
		t.Fatal(err)
	}
	id := mustOutboxID(t, db, "a1")
	if err := repo.ExpireClaimable(context.Background(), id, "a1", "obsolete", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.ExpireClaimable(context.Background(), id, "a1", "obsolete", now); !errors.Is(err, out.ErrNotExpirable) {
		t.Fatalf("second ExpireClaimable() error = %v, want ErrNotExpirable", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM publication_outbox WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dead" {
		t.Errorf("status = %q, want dead", status)
	}
}

func TestOutboxExpirationRepository_FiltersContentTypeAndSource(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	repo := sqliteadapter.NewOutboxExpirationRepository(db)
	now := time.Now()
	for _, id := range []string{"a1", "a2"} {
		mustSaveArticleWithPublishedAt(t, articles, id, now.Add(-48*time.Hour))
		if err := outbox.Enqueue(context.Background(), id, "{}", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE articles SET content_type = 'video', source_id = 'rss:other' WHERE id = 'a2'`); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListExpirationCandidates(context.Background(), out.OutboxExpirationFilter{PublishedBefore: now, ContentType: article.ContentTypeVideo, SourceID: "rss:other", Limit: 10})
	if err != nil || len(candidates) != 1 || candidates[0].ArticleID != "a2" {
		t.Fatalf("candidates = %+v, %v, want only a2", candidates, err)
	}
}

func TestExpireOutbox_RollsBackWholeBatchOnPartialFailure(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	expirations := sqliteadapter.NewOutboxExpirationRepository(db)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a1", "a2"} {
		mustSaveArticleWithPublishedAt(t, articles, id, now.Add(-48*time.Hour))
		if err := outbox.Enqueue(context.Background(), id, "{}", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
		CREATE TRIGGER fail_second_expiration BEFORE INSERT ON article_processing_events
		WHEN NEW.article_id = 'a2' BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	candidates, err := expirations.ListExpirationCandidates(context.Background(), out.OutboxExpirationFilter{PublishedBefore: now, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	uc := usecase.NewExpireOutbox(expirations, articles, sqliteadapter.NewClusterPublicationRepository(db), sqliteadapter.NewSourcePolicyCooldownRepository(db), sqliteadapter.NewDecisionRepository(db), sqliteadapter.NewUnitOfWork(db), expirationTestClock{now: now})
	if _, err := uc.Apply(context.Background(), candidates, "obsolete"); err == nil {
		t.Fatal("Apply() error = nil, want injected audit failure")
	}
	for _, id := range []string{"a1", "a2"} {
		var outboxStatus, articleStatus string
		if err := db.QueryRow(`SELECT po.status, a.status FROM publication_outbox po JOIN articles a ON a.id = po.article_id WHERE a.id = ?`, id).Scan(&outboxStatus, &articleStatus); err != nil {
			t.Fatal(err)
		}
		if outboxStatus != "pending" || articleStatus != "fetched" {
			t.Errorf("%s statuses = %s/%s, want pending/fetched after rollback", id, outboxStatus, articleStatus)
		}
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM article_processing_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("events = %d, want 0 after rollback", events)
	}
}
