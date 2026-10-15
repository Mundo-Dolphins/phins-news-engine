package sqlite_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func TestHumanLabelRepository_Label_FirstLabelSucceeds(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledBy: "reviewer-1", LabeledAt: now, Source: "cli:label"}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}

	active, ok, err := repo.GetActive(ctx, "a1")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if !ok {
		t.Fatal("GetActive() ok = false, want true after Label")
	}
	if active.Label != "publish" || active.Revision != 1 || active.Supersedes != 0 {
		t.Errorf("GetActive() = %+v, want Label=publish Revision=1 Supersedes=0", active)
	}
	if active.ContentHash == "" {
		t.Error("GetActive().ContentHash is empty, want a computed sha256 hash")
	}
}

func TestHumanLabelRepository_Label_SecondCallFailsWithAlreadyLabeled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledAt: now.Add(time.Hour)})
	if !errors.Is(err, training.ErrAlreadyLabeled) {
		t.Fatalf("second Label() error = %v, want training.ErrAlreadyLabeled", err)
	}
}

func TestHumanLabelRepository_RepeatedSameLabel_CreatesNewRevisionViaRelabel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledBy: "reviewer-1", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	if err := repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledBy: "reviewer-2", LabeledAt: now.Add(time.Hour)}, 1); err != nil {
		t.Fatalf("Relabel() error = %v", err)
	}

	active, ok, err := repo.GetActive(ctx, "a1")
	if err != nil || !ok {
		t.Fatalf("GetActive() = (%+v, %v, %v)", active, ok, err)
	}
	if active.Revision != 2 || active.Supersedes != 1 || active.Label != "publish" || active.LabeledBy != "reviewer-2" {
		t.Errorf("GetActive() = %+v, want Revision=2 Supersedes=1 Label=publish LabeledBy=reviewer-2", active)
	}

	history, err := repo.ListByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("len(history) = %d, want 2 (both revisions preserved)", len(history))
	}
}

func TestHumanLabelRepository_Relabel_OppositeLabel_OnlyOneActiveAndExported(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	if err := repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledAt: now.Add(time.Hour)}, 1); err != nil {
		t.Fatalf("Relabel() error = %v", err)
	}

	examples, err := repo.ListEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("ListEditorialExamples() error = %v", err)
	}
	// This is the fix: exactly one active example survives,
	// not two contradictory ones.
	if len(examples) != 1 {
		t.Fatalf("len(examples) = %d, want 1", len(examples))
	}
	if examples[0].Label != "reject" || examples[0].Revision != 2 {
		t.Errorf("examples[0] = %+v, want Label=reject Revision=2", examples[0])
	}

	count, err := repo.CountEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("CountEditorialExamples() error = %v", err)
	}
	if count != 2 {
		t.Errorf("CountEditorialExamples() = %d, want 2 (both revisions retained, never deleted)", count)
	}
}

func TestHumanLabelRepository_Relabel_WrongExpectedRevisionFailsWithConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	err := repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledAt: now.Add(time.Hour)}, 99)
	if !errors.Is(err, training.ErrRevisionConflict) {
		t.Fatalf("Relabel(wrong revision) error = %v, want training.ErrRevisionConflict", err)
	}
}

func TestHumanLabelRepository_Relabel_NoActiveLabelFailsWithNotLabeled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)

	err := repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: time.Now()}, 1)
	if !errors.Is(err, training.ErrNotLabeled) {
		t.Fatalf("Relabel() on unlabeled article error = %v, want training.ErrNotLabeled", err)
	}
}

func TestHumanLabelRepository_TwoConcurrentRelabels_OneWinsOneConflicts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledBy: "writer-a", LabeledAt: now.Add(time.Hour)}, 1)
	}()
	go func() {
		defer wg.Done()
		errs[1] = repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledBy: "writer-b", LabeledAt: now.Add(2 * time.Hour)}, 1)
	}()
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, training.ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error from concurrent Relabel: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want exactly one of each -- both writers targeted revision 1, only one may win", successes, conflicts)
	}

	var activeCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_labels WHERE article_id = 'a1' AND active = 1`).Scan(&activeCount); err != nil {
		t.Fatalf("count active rows: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active human_labels rows for a1 = %d, want 1 (the unique index must never allow two)", activeCount)
	}
}

func TestHumanLabelRepository_Retract_ClearsActiveLabelWithoutDeletingHistory(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}

	if err := repo.Retract(ctx, "a1", 1); err != nil {
		t.Fatalf("Retract() error = %v", err)
	}

	_, ok, err := repo.GetActive(ctx, "a1")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if ok {
		t.Error("GetActive() ok = true after Retract, want false")
	}
	history, err := repo.ListByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("len(history) = %d, want 1 (Retract must not delete the retracted row)", len(history))
	}
	examples, err := repo.ListEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("ListEditorialExamples() error = %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("len(examples) = %d, want 0 (a retracted article has no active dataset row)", len(examples))
	}
}

func TestHumanLabelRepository_Relabel_AfterContentChange_RecordsDifferentContentHash(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	articles := sqliteadapter.NewArticleRepository(db)
	base := article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source: source.Source{ID: mustID(t, source.TypeRSS, "s1"), Type: source.TypeRSS},
		Domain: "example.com", Title: "Original title", Summary: "Original body",
		ContentType: article.ContentTypeArticle, FetchedAt: time.Now(), PublishedAt: time.Now(),
		Status: article.StatusFetched,
	}
	if err := articles.Save(ctx, base); err != nil {
		t.Fatalf("save article: %v", err)
	}
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	firstActive, _, _ := repo.GetActive(ctx, "a1")

	base.Title = "Updated after correction"
	base.Summary = "Updated body"
	if err := articles.Save(ctx, base); err != nil {
		t.Fatalf("re-save article with new content: %v", err)
	}
	if err := repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now.Add(time.Hour)}, 1); err != nil {
		t.Fatalf("Relabel() error = %v", err)
	}
	secondActive, _, _ := repo.GetActive(ctx, "a1")

	if firstActive.ContentHash == secondActive.ContentHash {
		t.Error("ContentHash unchanged across a relabel that followed a real content update, want it to differ")
	}
}

func TestHumanLabelRepository_ResolveConflict_ActivatesNewRevisionAndMarksResolved(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	if _, err := db.ExecContext(ctx, `INSERT INTO human_labels (article_id, label, labeled_at, revision, active) VALUES ('a1', 'publish', '2026-01-01T00:00:00Z', 1, 0)`); err != nil {
		t.Fatalf("seed legacy row 1: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO human_labels (article_id, label, labeled_at, revision, active) VALUES ('a1', 'reject', '2026-01-01T00:00:00Z', 2, 0)`); err != nil {
		t.Fatalf("seed legacy row 2: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO human_label_conflicts (table_name, article_id, detail) VALUES ('human_labels', 'a1', 'seeded for test')`); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}
	repo := sqliteadapter.NewHumanLabelRepository(db)

	if err := repo.ResolveConflict(ctx, "a1", training.Example{ArticleID: "a1", Label: "publish", LabeledBy: "operator", LabeledAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Notes: "manually resolved"}); err != nil {
		t.Fatalf("ResolveConflict() error = %v", err)
	}

	active, ok, err := repo.GetActive(ctx, "a1")
	if err != nil || !ok || active.Label != "publish" {
		t.Fatalf("GetActive() = (%+v, %v, %v), want an active publish revision", active, ok, err)
	}

	unresolved, err := repo.ListUnresolvedConflicts(ctx)
	if err != nil {
		t.Fatalf("ListUnresolvedConflicts() error = %v", err)
	}
	for _, c := range unresolved {
		if c.ArticleID == "a1" {
			t.Errorf("a1 still has an unresolved conflict after ResolveConflict: %+v", c)
		}
	}
}

func TestHumanLabelRepository_ResolveConflict_PurgedArticle_ResolvesWithoutNewRevision(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// Deliberately no articles row: migration 0026 flags ambiguity in
	// editorial_examples independently of human_labels, and
	// editorial_examples survives a retention purge that cascade-deletes
	// the article. Such a conflict must still be resolvable, or its
	// `train export` warning is permanent.
	if _, err := db.ExecContext(ctx, `INSERT INTO human_label_conflicts (table_name, article_id, detail) VALUES ('editorial_examples', 'gone', 'seeded for test')`); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}
	repo := sqliteadapter.NewHumanLabelRepository(db)

	if err := repo.ResolveConflict(ctx, "gone", training.Example{ArticleID: "gone", Label: "publish", LabeledBy: "operator", LabeledAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("ResolveConflict() error = %v, want success for a conflict whose article was retention-purged", err)
	}

	unresolved, err := repo.ListUnresolvedConflicts(ctx)
	if err != nil {
		t.Fatalf("ListUnresolvedConflicts() error = %v", err)
	}
	for _, c := range unresolved {
		if c.ArticleID == "gone" {
			t.Errorf("gone still has an unresolved conflict after ResolveConflict: %+v", c)
		}
	}

	_, hasActive, err := repo.GetActive(ctx, "gone")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if hasActive {
		t.Error("GetActive() reports an active revision, want none (the article's content is gone, so there is nothing exportable to activate)")
	}
}

func TestHumanLabelRepository_CountUnresolvedConflicts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO human_label_conflicts (table_name, article_id, detail) VALUES ('human_labels', 'a1', 'd1'), ('editorial_examples', 'a1', 'd2'), ('human_labels', 'a2', 'd3')`); err != nil {
		t.Fatalf("seed conflicts: %v", err)
	}
	repo := sqliteadapter.NewHumanLabelRepository(db)

	n, err := repo.CountUnresolvedConflicts(ctx)
	if err != nil {
		t.Fatalf("CountUnresolvedConflicts() error = %v", err)
	}
	// a1 has two conflict rows (one per table) but counts once -- the
	// metric is "distinct article_ids with an unresolved conflict".
	if n != 2 {
		t.Errorf("CountUnresolvedConflicts() = %d, want 2", n)
	}
}

func TestHumanLabelRepository_Label_UnknownArticleFailsAtomically(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := sqliteadapter.NewHumanLabelRepository(db)

	err := repo.Label(ctx, training.Example{ArticleID: "does-not-exist", Label: "publish", LabeledAt: time.Now()})
	if err == nil {
		t.Fatal("Label() error = nil, want an error for a label on a nonexistent article")
	}
	all, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(all) != 0 {
		t.Errorf("ListAll() = %+v, want no human_labels row left behind by the failed Label", all)
	}
}

func TestHumanLabelRepository_EditorialExamples_SurviveArticleDeletion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "reject", Reason: "duplicate", LabeledAt: time.Now()}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM articles WHERE id = 'a1'`); err != nil {
		t.Fatalf("delete article: %v", err)
	}

	all, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(all) != 0 {
		t.Errorf("ListAll() = %+v, want the human_labels row gone (cascade-deleted with its article)", all)
	}
	examples, err := repo.ListEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("ListEditorialExamples() error = %v", err)
	}
	if len(examples) != 1 || examples[0].ArticleID != "a1" {
		t.Errorf("ListEditorialExamples() = %+v, want the durable example to survive the article's deletion", examples)
	}
}

func TestHumanLabelRepository_Relabel_SelfHealsDivergentEditorialExamplesRevision(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}

	// Simulate a migration-produced divergent state: human_labels' active
	// row is at revision 1, but editorial_examples' active row is at a
	// different revision number (per the design spec, migration 0026 ranks
	// each table's revisions independently, so this can legitimately
	// happen for an article whose human_labels history was cascade-deleted
	// by a retention purge while editorial_examples' history survived).
	if _, err := db.ExecContext(ctx, `UPDATE editorial_examples SET revision = 99 WHERE article_id = 'a1' AND active = 1`); err != nil {
		t.Fatalf("simulate divergence: %v", err)
	}

	if err := repo.Relabel(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledAt: now.Add(time.Hour)}, 1); err != nil {
		t.Fatalf("Relabel() error = %v, want success even with a divergent editorial_examples revision", err)
	}

	examples, err := repo.ListEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("ListEditorialExamples() error = %v", err)
	}
	if len(examples) != 1 || examples[0].Label != "reject" {
		t.Fatalf("ListEditorialExamples() = %+v, want exactly one active reject example", examples)
	}

	var activeCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM editorial_examples WHERE article_id = 'a1' AND active = 1`).Scan(&activeCount); err != nil {
		t.Fatalf("count active rows: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active editorial_examples rows for a1 = %d, want 1", activeCount)
	}
}

func TestHumanLabelRepository_Label_ResyncsStaleEditorialExamplesAfterArticlePurgeAndReingest(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "reject", LabeledAt: time.Now()}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	// Simulate a retention purge: human_labels cascades away with the
	// article; editorial_examples survives (no FK) with its active row
	// still active=1.
	if _, err := db.ExecContext(ctx, `DELETE FROM articles WHERE id = 'a1'`); err != nil {
		t.Fatalf("delete article: %v", err)
	}
	// Re-ingest the same article_id (stable ingestion identity, #154).
	insertTestArticle(t, db, "a1")

	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: time.Now()}); err != nil {
		t.Fatalf("Label() after re-ingest error = %v, want success (must resync the stale editorial_examples active row)", err)
	}

	var activeCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM editorial_examples WHERE article_id = 'a1' AND active = 1`).Scan(&activeCount); err != nil {
		t.Fatalf("count active rows: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active editorial_examples rows for a1 = %d, want 1", activeCount)
	}
}

func importSnapshot(id, label string) training.EditorialExample {
	return training.EditorialExample{
		ArticleID: id, Label: label, Reason: "quality", LabeledBy: "javi", Source: "dataset_review",
		LabeledAt: time.Date(2026, 9, 29, 13, 20, 0, 0, time.UTC),
		Title:     "Snapshot title " + id, Body: "snapshot body", SourceID: "twitter:hashtag-finsup", SourceType: "twitter",
		ContentType: "social_post", PublishedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), FetchedAt: time.Date(2026, 9, 5, 0, 1, 0, 0, time.UTC),
	}
}

func TestHumanLabelRepository_ImportEditorialExample_InsertsSnapshotForPurgedArticle(t *testing.T) {
	// The article row is long gone (retention); only the snapshot survives.
	db := newTestDB(t)
	ctx := context.Background()
	repo := sqliteadapter.NewHumanLabelRepository(db)

	outcome, err := repo.ImportEditorialExample(ctx, importSnapshot("gone", "reject"), false)
	if err != nil {
		t.Fatalf("ImportEditorialExample() error = %v", err)
	}
	if outcome != sqliteadapter.ImportInserted {
		t.Errorf("outcome = %v, want ImportInserted", outcome)
	}
	examples, err := repo.ListEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("ListEditorialExamples() error = %v", err)
	}
	if len(examples) != 1 || examples[0].Label != "reject" || examples[0].Title != "Snapshot title gone" || examples[0].ContentHash == "" || examples[0].Revision != 1 {
		t.Errorf("examples = %+v, want one active revision-1 reject snapshot with a content hash", examples)
	}
}

func TestHumanLabelRepository_ImportEditorialExample_SameLabelIsUnchanged(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := sqliteadapter.NewHumanLabelRepository(db)
	if _, err := repo.ImportEditorialExample(ctx, importSnapshot("a1", "reject"), false); err != nil {
		t.Fatalf("first import error = %v", err)
	}
	outcome, err := repo.ImportEditorialExample(ctx, importSnapshot("a1", "reject"), true)
	if err != nil {
		t.Fatalf("second import error = %v", err)
	}
	if outcome != sqliteadapter.ImportUnchanged {
		t.Errorf("outcome = %v, want ImportUnchanged", outcome)
	}
}

func TestHumanLabelRepository_ImportEditorialExample_DifferentLabelWithoutOverrideIsConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: time.Now(), Source: "telegramimport"}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	outcome, err := repo.ImportEditorialExample(ctx, importSnapshot("a1", "reject"), false)
	if err != nil {
		t.Fatalf("ImportEditorialExample() error = %v", err)
	}
	if outcome != sqliteadapter.ImportConflict {
		t.Errorf("outcome = %v, want ImportConflict", outcome)
	}
	active, _, _ := repo.GetActive(ctx, "a1")
	if active.Label != "publish" {
		t.Errorf("active label = %q, want publish left untouched", active.Label)
	}
}

func TestHumanLabelRepository_ImportEditorialExample_OverrideSupersedesBothTables(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertTestArticle(t, db, "a1")
	repo := sqliteadapter.NewHumanLabelRepository(db)
	if err := repo.Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: time.Now(), Source: "telegramimport"}); err != nil {
		t.Fatalf("Label() error = %v", err)
	}
	outcome, err := repo.ImportEditorialExample(ctx, importSnapshot("a1", "reject"), true)
	if err != nil {
		t.Fatalf("ImportEditorialExample() error = %v", err)
	}
	if outcome != sqliteadapter.ImportSuperseded {
		t.Errorf("outcome = %v, want ImportSuperseded", outcome)
	}
	examples, _ := repo.ListEditorialExamples(ctx)
	if len(examples) != 1 || examples[0].Label != "reject" || examples[0].Revision != 2 || examples[0].Supersedes != 1 {
		t.Errorf("active examples = %+v, want one reject at revision 2 superseding 1", examples)
	}
	// human_labels must not keep a contradicting active publish row.
	if active, ok, _ := repo.GetActive(ctx, "a1"); ok && active.Label != "reject" {
		t.Errorf("human_labels active = %+v, want no contradicting active label", active)
	}
	history, _ := repo.ListByArticle(ctx, "a1")
	if len(history) == 0 {
		t.Error("human_labels history empty, want the superseded publish revision kept")
	}
}
