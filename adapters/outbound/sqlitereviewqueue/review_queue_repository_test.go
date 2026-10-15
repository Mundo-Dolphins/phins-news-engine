// adapters/outbound/sqlitereviewqueue/review_queue_repository_test.go
package sqlitereviewqueue_test

import (
	"context"
	"testing"

	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
)

func TestReviewQueueRepository_UpsertThenList(t *testing.T) {
	db := newTestDB(t)
	repo := sqlitereviewqueue.NewReviewQueueRepository(db)
	ctx := context.Background()

	low := reviewpriority.QueueEntry{
		ArticleID: "article-low", ProposalID: "proposal-low", Score: 0.2, Reason: "low",
		NBZone: editorial.ZoneAmbiguous, NBValue: 50, LLMLabel: "uncertain", LLMConfidence: 0.5, SuggestedLabel: "uncertain",
	}
	high := reviewpriority.QueueEntry{
		ArticleID: "article-high", ProposalID: "proposal-high", Score: 0.9, Reason: "high",
		NBZone: editorial.ZoneAccept, NBValue: 90, LLMLabel: "discard", LLMConfidence: 0.6, SuggestedLabel: "discard",
	}
	if err := repo.Upsert(ctx, low); err != nil {
		t.Fatalf("Upsert(low) error = %v", err)
	}
	if err := repo.Upsert(ctx, high); err != nil {
		t.Fatalf("Upsert(high) error = %v", err)
	}

	got, err := repo.ListOrderedByScore(ctx, 10)
	if err != nil {
		t.Fatalf("ListOrderedByScore() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].ArticleID != "article-high" || got[1].ArticleID != "article-low" {
		t.Errorf("order = [%s, %s], want [article-high, article-low] (highest score first)", got[0].ArticleID, got[1].ArticleID)
	}
	if got[0].Reason != "high" || got[0].NBZone != editorial.ZoneAccept || got[0].LLMLabel != "discard" {
		t.Errorf("got[0] = %+v, fields did not round-trip", got[0])
	}
}

func TestReviewQueueRepository_UpsertReplacesExistingRow(t *testing.T) {
	db := newTestDB(t)
	repo := sqlitereviewqueue.NewReviewQueueRepository(db)
	ctx := context.Background()

	entry := reviewpriority.QueueEntry{ArticleID: "article-1", ProposalID: "proposal-1", Score: 0.3, Reason: "first", NBZone: editorial.ZoneAmbiguous, LLMLabel: "uncertain", SuggestedLabel: "uncertain"}
	if err := repo.Upsert(ctx, entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	entry.Score = 0.8
	entry.Reason = "rescored"
	if err := repo.Upsert(ctx, entry); err != nil {
		t.Fatalf("Upsert() (replace) error = %v", err)
	}

	got, err := repo.ListOrderedByScore(ctx, 10)
	if err != nil {
		t.Fatalf("ListOrderedByScore() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (upsert must replace, not duplicate)", len(got))
	}
	if got[0].Score != 0.8 || got[0].Reason != "rescored" {
		t.Errorf("got[0] = %+v, want the rescored values", got[0])
	}
}

func TestReviewQueueRepository_Delete(t *testing.T) {
	db := newTestDB(t)
	repo := sqlitereviewqueue.NewReviewQueueRepository(db)
	ctx := context.Background()

	entry := reviewpriority.QueueEntry{ArticleID: "article-1", ProposalID: "proposal-1", Score: 0.5, NBZone: editorial.ZoneAmbiguous, LLMLabel: "uncertain", SuggestedLabel: "uncertain"}
	if err := repo.Upsert(ctx, entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := repo.Delete(ctx, "article-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	// Deleting a non-existent row must not error (idempotent).
	if err := repo.Delete(ctx, "article-does-not-exist"); err != nil {
		t.Fatalf("Delete() on missing row error = %v, want nil", err)
	}

	got, err := repo.ListOrderedByScore(ctx, 10)
	if err != nil {
		t.Fatalf("ListOrderedByScore() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 after Delete", len(got))
	}
}
