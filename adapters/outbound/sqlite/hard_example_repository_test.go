package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func TestHardExampleRepository_UpsertAndListUnresolved(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")
	repo := sqliteadapter.NewHardExampleRepository(db)
	ctx := context.Background()
	minedAt := time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)

	if err := repo.Upsert(ctx, training.HardExampleCandidate{
		ArticleID: "a1", Kind: "negative", NBScore: 0.92, LLMLabel: "discard", LLMConfidence: 0.95, MinedAt: minedAt,
	}); err != nil {
		t.Fatalf("Upsert(a1) error = %v", err)
	}
	if err := repo.Upsert(ctx, training.HardExampleCandidate{
		ArticleID: "a2", Kind: "positive", NBScore: 0.05, LLMLabel: "publish", LLMConfidence: 0.90, MinedAt: minedAt,
	}); err != nil {
		t.Fatalf("Upsert(a2) error = %v", err)
	}

	count, err := repo.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("UnresolvedCount() = %d, want 2", count)
	}

	got, err := repo.ListUnresolved(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnresolved() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListUnresolved() len = %d, want 2", len(got))
	}
	byID := map[string]training.HardExampleCandidate{}
	for _, c := range got {
		byID[c.ArticleID] = c
	}
	if byID["a1"].Kind != "negative" || byID["a1"].LLMLabel != "discard" {
		t.Errorf("a1 = %+v, want kind=negative llm_label=discard", byID["a1"])
	}
	if byID["a1"].ResolvedAt != nil {
		t.Errorf("a1.ResolvedAt = %v, want nil (unresolved)", byID["a1"].ResolvedAt)
	}
}

func TestHardExampleRepository_UpsertRefreshesExistingRow(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	mustSaveArticle(t, articles, "a1")
	repo := sqliteadapter.NewHardExampleRepository(db)
	ctx := context.Background()

	if err := repo.Upsert(ctx, training.HardExampleCandidate{
		ArticleID: "a1", Kind: "negative", NBScore: 0.80, LLMLabel: "discard", LLMConfidence: 0.85,
		MinedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("first Upsert error = %v", err)
	}
	if err := repo.Upsert(ctx, training.HardExampleCandidate{
		ArticleID: "a1", Kind: "negative", NBScore: 0.93, LLMLabel: "discard", LLMConfidence: 0.97,
		MinedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("second Upsert error = %v", err)
	}

	count, err := repo.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount() error = %v", err)
	}
	if count != 1 {
		t.Errorf("UnresolvedCount() = %d, want 1 (re-mining upserts, does not duplicate)", count)
	}

	got, err := repo.ListUnresolved(ctx, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListUnresolved() = %+v, err = %v, want exactly 1 row", got, err)
	}
	if got[0].NBScore != 0.93 || got[0].LLMConfidence != 0.97 {
		t.Errorf("ListUnresolved()[0] = %+v, want refreshed scores (0.93, 0.97)", got[0])
	}
}

func TestHardExampleRepository_MarkResolved(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")
	repo := sqliteadapter.NewHardExampleRepository(db)
	ctx := context.Background()

	for _, id := range []string{"a1", "a2"} {
		if err := repo.Upsert(ctx, training.HardExampleCandidate{
			ArticleID: id, Kind: "negative", NBScore: 0.9, LLMLabel: "discard", LLMConfidence: 0.9, MinedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Upsert(%s) error = %v", id, err)
		}
	}

	resolvedAt := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	if err := repo.MarkResolved(ctx, "a1", resolvedAt); err != nil {
		t.Fatalf("MarkResolved() error = %v", err)
	}

	count, err := repo.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount() error = %v", err)
	}
	if count != 1 {
		t.Errorf("UnresolvedCount() = %d, want 1 (a1 resolved, a2 still unresolved)", count)
	}

	got, err := repo.ListUnresolved(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnresolved() error = %v", err)
	}
	if len(got) != 1 || got[0].ArticleID != "a2" {
		t.Errorf("ListUnresolved() = %+v, want only a2", got)
	}
}

// TestHardExampleRepository_IsUnresolved covers the whole-branch
// review's Finding 1: application/editorialreview.Sync needs a
// per-article existence check to populate
// reviewpriority.Signals.HardMiningCount.
func TestHardExampleRepository_IsUnresolved(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")
	repo := sqliteadapter.NewHardExampleRepository(db)
	ctx := context.Background()

	if err := repo.Upsert(ctx, training.HardExampleCandidate{
		ArticleID: "a1", Kind: "negative", NBScore: 0.9, LLMLabel: "discard", LLMConfidence: 0.9, MinedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert(a1) error = %v", err)
	}

	got, err := repo.IsUnresolved(ctx, "a1")
	if err != nil {
		t.Fatalf("IsUnresolved(a1) error = %v", err)
	}
	if !got {
		t.Error("IsUnresolved(a1) = false, want true (a1 has an unresolved candidate row)")
	}

	// An article with no candidate row at all is false, not an error.
	got, err = repo.IsUnresolved(ctx, "a2")
	if err != nil {
		t.Fatalf("IsUnresolved(a2) error = %v", err)
	}
	if got {
		t.Error("IsUnresolved(a2) = true, want false (a2 has no candidate row)")
	}

	// An already-resolved row is false too -- Sync must not keep boosting
	// an article a human has already dealt with.
	if err := repo.MarkResolved(ctx, "a1", time.Now().UTC()); err != nil {
		t.Fatalf("MarkResolved(a1) error = %v", err)
	}
	got, err = repo.IsUnresolved(ctx, "a1")
	if err != nil {
		t.Fatalf("IsUnresolved(a1) after resolve error = %v", err)
	}
	if got {
		t.Error("IsUnresolved(a1) = true after MarkResolved, want false")
	}
}

// TestHardExampleRepository_ArticleDeleteCascades covers the
// whole-branch review's Finding 3: hard_example_candidates' FK to
// articles(id) originally specified neither ON DELETE CASCADE nor SET
// NULL, so with foreign keys enforced on every connection a retention
// purge's single all-or-nothing `DELETE FROM articles` would abort as
// soon as one purgeable article had a candidate row.
func TestHardExampleRepository_ArticleDeleteCascades(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	mustSaveArticle(t, articles, "a1")
	repo := sqliteadapter.NewHardExampleRepository(db)
	ctx := context.Background()

	if err := repo.Upsert(ctx, training.HardExampleCandidate{
		ArticleID: "a1", Kind: "negative", NBScore: 0.9, LLMLabel: "discard", LLMConfidence: 0.9, MinedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert(a1) error = %v", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM articles WHERE id = ?`, "a1"); err != nil {
		t.Fatalf("DELETE FROM articles error = %v, want the candidate row to cascade away instead", err)
	}

	count, err := repo.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount() error = %v", err)
	}
	if count != 0 {
		t.Errorf("UnresolvedCount() = %d, want 0 (a1's candidate row should have cascaded)", count)
	}
}
