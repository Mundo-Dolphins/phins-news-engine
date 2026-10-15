package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// seedSessionAndArticles (defined in review_decision_repository_test.go,
// shared across this package's _test.go files) creates a running
// editorial cycle session and one article per id in articleIDs, ready to
// be frozen into that session's candidate set by the test.

func TestEditorialCycleSessionCandidateRepository_SaveAllThenListPending(t *testing.T) {
	db := newTestDB(t)
	seedSessionAndArticles(t, db, "sess1", "c1", "c2")

	repo := sqliteadapter.NewEditorialCycleSessionCandidateRepository(db)
	now := time.Now().UTC()
	if err := repo.SaveAll(context.Background(), "sess1", []appout.EditorialCycleSessionCandidate{
		{SessionID: "sess1", ArticleID: "c1", Score: 3.5, SelectedAt: now},
		{SessionID: "sess1", ArticleID: "c2", Score: 1.0, SelectedAt: now},
	}); err != nil {
		t.Fatalf("SaveAll() error = %v", err)
	}

	pending, err := repo.ListPending(context.Background(), "sess1")
	if err != nil {
		t.Fatalf("ListPending() error = %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("ListPending() = %+v, want 2 (nothing decided yet)", pending)
	}

	frozen, err := repo.ListFrozen(context.Background(), "sess1")
	if err != nil {
		t.Fatalf("ListFrozen() error = %v", err)
	}
	if len(frozen) != 2 {
		t.Fatalf("ListFrozen() = %+v, want 2", frozen)
	}

	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
	if err := reviewDecisions.Save(context.Background(), appout.ReviewDecision{
		SessionID: "sess1", ArticleID: "c1", Action: "publish", Label: "publish", DecidedAt: now,
	}); err != nil {
		t.Fatalf("save decision: %v", err)
	}

	pending, err = repo.ListPending(context.Background(), "sess1")
	if err != nil {
		t.Fatalf("ListPending() (after decision) error = %v", err)
	}
	if len(pending) != 1 || pending[0].ArticleID != "c2" {
		t.Fatalf("ListPending() = %+v, want only c2 pending after c1 was decided", pending)
	}

	// ListFrozen ignores decision state entirely — both candidates remain
	// visible for --redecide/drift-auditing even after c1 was decided.
	frozen, err = repo.ListFrozen(context.Background(), "sess1")
	if err != nil {
		t.Fatalf("ListFrozen() (after decision) error = %v", err)
	}
	if len(frozen) != 2 {
		t.Fatalf("ListFrozen() = %+v, want 2 (decision state must not affect the frozen set)", frozen)
	}
}

func TestEditorialCycleSessionCandidateRepository_ListPending_ExcludesPurgedArticles(t *testing.T) {
	db := newTestDB(t)
	seedSessionAndArticles(t, db, "sess2", "p1", "p2")
	repo := sqliteadapter.NewEditorialCycleSessionCandidateRepository(db)
	now := time.Now().UTC()
	if err := repo.SaveAll(context.Background(), "sess2", []appout.EditorialCycleSessionCandidate{
		{SessionID: "sess2", ArticleID: "p1", Score: 1, SelectedAt: now},
		{SessionID: "sess2", ArticleID: "p2", Score: 1, SelectedAt: now},
	}); err != nil {
		t.Fatalf("SaveAll() error = %v", err)
	}
	if _, err := db.Exec(`DELETE FROM articles WHERE id = 'p1'`); err != nil {
		t.Fatalf("delete article: %v", err)
	}

	pending, err := repo.ListPending(context.Background(), "sess2")
	if err != nil {
		t.Fatalf("ListPending() error = %v", err)
	}
	if len(pending) != 1 || pending[0].ArticleID != "p2" {
		t.Fatalf("ListPending() = %+v, want only p2 (p1 was purged and cascaded out of the frozen set)", pending)
	}

	n, err := repo.CountResolvable(context.Background(), "sess2")
	if err != nil {
		t.Fatalf("CountResolvable() error = %v", err)
	}
	if n != 1 {
		t.Errorf("CountResolvable() = %d, want 1", n)
	}

	// editorial_cycle_session_candidates.article_id is ON DELETE CASCADE,
	// so deleting an article deletes its frozen-candidate row right along
	// with it — ListFrozen can therefore never show a row for a purged
	// article, despite "frozen" otherwise suggesting an unwary reader
	// should expect p1 to still be listed here. This same CASCADE
	// behavior is why migration 0025's total_candidates column exists at
	// all: ListFrozen and CountResolvable can never disagree once a row
	// is gone outright rather than merely orphaned, so total_candidates
	// is the only durable record that p1 was ever part of this session's
	// frozen set (see session.go's StartOrResume/Plan.DriftWarning
	// comments for how that baseline is used).
	frozen, err := repo.ListFrozen(context.Background(), "sess2")
	if err != nil {
		t.Fatalf("ListFrozen() error = %v", err)
	}
	if len(frozen) != 1 || frozen[0].ArticleID != "p2" {
		t.Fatalf("ListFrozen() = %+v, want only p2 (p1's candidate row was cascade-deleted when its article was deleted)", frozen)
	}
}

func TestEditorialCycleSessionCandidateRepository_CountResolvable_AllPurged(t *testing.T) {
	db := newTestDB(t)
	seedSessionAndArticles(t, db, "sess3", "q1", "q2")
	repo := sqliteadapter.NewEditorialCycleSessionCandidateRepository(db)
	now := time.Now().UTC()
	if err := repo.SaveAll(context.Background(), "sess3", []appout.EditorialCycleSessionCandidate{
		{SessionID: "sess3", ArticleID: "q1", Score: 1, SelectedAt: now},
		{SessionID: "sess3", ArticleID: "q2", Score: 1, SelectedAt: now},
	}); err != nil {
		t.Fatalf("SaveAll() error = %v", err)
	}
	if _, err := db.Exec(`DELETE FROM articles WHERE id IN ('q1','q2')`); err != nil {
		t.Fatalf("delete articles: %v", err)
	}

	n, err := repo.CountResolvable(context.Background(), "sess3")
	if err != nil {
		t.Fatalf("CountResolvable() error = %v", err)
	}
	if n != 0 {
		t.Errorf("CountResolvable() = %d, want 0 (every frozen candidate was purged)", n)
	}

	pending, err := repo.ListPending(context.Background(), "sess3")
	if err != nil {
		t.Fatalf("ListPending() error = %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("ListPending() = %+v, want empty", pending)
	}
}
