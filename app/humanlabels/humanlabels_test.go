package humanlabels_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/humanlabels"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

type fakeHard struct{ resolved []string }

func (f *fakeHard) MarkResolved(_ context.Context, id string, _ time.Time) error {
	f.resolved = append(f.resolved, id)
	return nil
}

func setup(t *testing.T) (*sql.DB, *sqliteadapter.HumanLabelRepository) {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	now := time.Now().UTC()
	if err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
	}); err != nil {
		t.Fatal(err)
	}
	return db, sqliteadapter.NewHumanLabelRepository(db)
}

func ex(label string) training.Example {
	return training.Example{ArticleID: "a1", Label: label, LabeledBy: "t", LabeledAt: time.Now().UTC(), Source: "test"}
}

func TestSetNewLabel(t *testing.T) {
	_, repo := setup(t)
	out, warn, err := humanlabels.Set(context.Background(), repo, &fakeHard{}, ex("publish"))
	if err != nil || warn != nil {
		t.Fatalf("err=%v warn=%v", err, warn)
	}
	if out.Relabeled {
		t.Errorf("outcome = %+v", out)
	}
}

func TestSetRelabelBumpsRevision(t *testing.T) {
	_, repo := setup(t)
	ctx := context.Background()
	if _, _, err := humanlabels.Set(ctx, repo, &fakeHard{}, ex("reject")); err != nil {
		t.Fatal(err)
	}
	out, _, err := humanlabels.Set(ctx, repo, &fakeHard{}, ex("publish"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Relabeled || out.Previous != "reject" || out.Revision != 2 {
		t.Errorf("outcome = %+v", out)
	}
}

func TestSetRefusesUnresolvedConflict(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO human_label_conflicts (table_name, article_id, detail, detected_at) VALUES ('human_labels', 'a1', 'x', ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, _, err := humanlabels.Set(ctx, repo, &fakeHard{}, ex("publish"))
	if !errors.Is(err, humanlabels.ErrUnresolvedConflict) {
		t.Fatalf("err = %v", err)
	}
	want := `label: a1 has an unresolved legacy label conflict -- use "label resolve-conflict a1 publish" instead of "label set"`
	if err.Error() != want {
		t.Errorf("text = %q", err.Error())
	}
	hist, _ := humanlabels.History(ctx, repo, "a1")
	if len(hist) != 0 {
		t.Errorf("rows written: %v", hist)
	}
}

func TestSetMarksHardExampleResolved(t *testing.T) {
	_, repo := setup(t)
	hard := &fakeHard{}
	if _, _, err := humanlabels.Set(context.Background(), repo, hard, ex("publish")); err != nil {
		t.Fatal(err)
	}
	if len(hard.resolved) != 1 || hard.resolved[0] != "a1" {
		t.Errorf("resolved = %v", hard.resolved)
	}
}

type fakeLister struct{ articles []article.Article }

func (f fakeLister) ListByStatusUnlabeled(context.Context, []article.Status, int) ([]article.Article, error) {
	return f.articles, nil
}

func rejected(t *testing.T, db *sql.DB, id string) article.Article {
	t.Helper()
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	now := time.Now().UTC()
	a := article.Article{
		ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusRejectedQuality,
	}
	if err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	db, repo := setup(t)
	rejected(t, db, "r1")
	rejected(t, db, "r2")
	rep, err := humanlabels.BackfillRejects(context.Background(), sqliteadapter.NewArticleRepository(db), repo, 100, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (humanlabels.BackfillReport{Candidates: 2}) {
		t.Errorf("report = %+v", rep)
	}
	if _, ok, _ := repo.GetActive(context.Background(), "r1"); ok {
		t.Error("dry run wrote a label")
	}
}

func TestBackfillSkipsAlreadyLabeled(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()
	r1, r2 := rejected(t, db, "r1"), rejected(t, db, "r2")
	if err := repo.Label(ctx, training.Example{ArticleID: "r2", Label: "publish", LabeledBy: "t", LabeledAt: time.Now().UTC(), Source: "test"}); err != nil {
		t.Fatal(err)
	}
	rep, err := humanlabels.BackfillRejects(ctx, fakeLister{[]article.Article{r1, r2}}, repo, 100, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (humanlabels.BackfillReport{Candidates: 2, Labeled: 1, Skipped: 1}) {
		t.Errorf("report = %+v", rep)
	}
	got, ok, _ := repo.GetActive(ctx, "r1")
	if !ok || got.Label != "reject" || got.Source != "pipeline_backfill" {
		t.Errorf("r1 = %+v ok=%v", got, ok)
	}
}

func TestImportFromDatasetOverride(t *testing.T) {
	_, repo := setup(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	if _, err := repo.ImportEditorialExample(ctx, training.EditorialExample{ArticleID: "x", Label: "reject", Source: "cli:label", Title: "t", LabeledAt: at, FetchedAt: at}, false); err != nil {
		t.Fatal(err)
	}
	ds := editorialtrain.Dataset{Examples: []editorialtrain.TrainingExample{
		{ArticleID: "x", Label: "publish", LabelSource: "interactive_review", Title: "t", LabeledAt: at, FetchedAt: at},
		{ArticleID: "y", Label: "publish", LabelSource: "interactive_review", Title: "t", LabeledAt: at, FetchedAt: at},
	}}
	tally, err := humanlabels.ImportFromDataset(ctx, repo, ds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tally != (humanlabels.ImportTally{Inserted: 1, Conflicts: 1}) {
		t.Errorf("no override: %+v", tally)
	}
	tally, err = humanlabels.ImportFromDataset(ctx, repo, ds, map[string]bool{"interactive_review": true})
	if err != nil {
		t.Fatal(err)
	}
	if tally != (humanlabels.ImportTally{Unchanged: 1, Superseded: 1}) {
		t.Errorf("override: %+v", tally)
	}
}

func TestImportFromDBCopiesAdditively(t *testing.T) {
	_, repo := setup(t)
	_, archive := setup(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	if _, err := archive.ImportEditorialExample(ctx, training.EditorialExample{ArticleID: "o", Label: "reject", Source: "cli:label", Title: "t", LabeledAt: at, FetchedAt: at}, false); err != nil {
		t.Fatal(err)
	}
	tally, err := humanlabels.ImportFromDB(ctx, repo, archive)
	if err != nil || tally != (humanlabels.ImportTally{Inserted: 1}) {
		t.Fatalf("tally = %+v err = %v", tally, err)
	}
	if got, want := tally.String(), "1 inserted, 0 unchanged, 0 superseded, 0 conflict(s) left as-is"; got != want {
		t.Errorf("String = %q", got)
	}
}
