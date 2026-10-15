package train_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func testSource(t *testing.T) source.Source {
	t.Helper()
	id, err := source.NewID(source.TypeRSS, "test-source")
	if err != nil {
		t.Fatalf("source.NewID() error = %v", err)
	}
	return source.Source{ID: id, Type: source.TypeRSS}
}

func TestBackfillRejectsCmd_LabelsEligibleUnlabeledArticles(t *testing.T) {
	configPath, _ := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: "reject-candidate-1", Title: "Shelbyville Sharks sign a new kicker",
		Status: article.StatusRejectedQuality, FetchedAt: time.Now().UTC(), Source: testSource(t),
	}); err != nil {
		db.Close()
		t.Fatalf("seed reject candidate: %v", err)
	}
	db.Close()

	out, err := testsupport.Execute(t, "--config", configPath, "train", "backfill-rejects")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "1 labeled") {
		t.Errorf("output = %q, want it to report 1 labeled", out)
	}

	db, err = infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()
	active, ok, err := sqliteadapter.NewHumanLabelRepository(db).GetActive(context.Background(), "reject-candidate-1")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if !ok || active.Label != "reject" || active.Source != "pipeline_backfill" {
		t.Errorf("GetActive() = %+v, ok=%v, want label=reject source=pipeline_backfill", active, ok)
	}
}

func TestBackfillRejectsCmd_DryRunLabelsNothing(t *testing.T) {
	configPath, _ := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: "reject-candidate-2", Title: "Badgers TE injury update",
		Status: article.StatusRejectedQuality, FetchedAt: time.Now().UTC(), Source: testSource(t),
	}); err != nil {
		db.Close()
		t.Fatalf("seed reject candidate: %v", err)
	}
	db.Close()

	out, err := testsupport.Execute(t, "--config", configPath, "train", "backfill-rejects", "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "0 labeled") {
		t.Errorf("output = %q, want it to report 0 labeled under --dry-run", out)
	}

	db, err = infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()
	_, ok, err := sqliteadapter.NewHumanLabelRepository(db).GetActive(context.Background(), "reject-candidate-2")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if ok {
		t.Error("GetActive() found an active label after --dry-run, want none")
	}
}

// TestBackfillRejectsCmd_NeverTouchesAlreadyLabeledArticleAndSecondRunIsIdempotent
// covers the plan's Review Focus requirement that backfill-rejects never
// touches an already-labeled article and that a second run is idempotent.
// The real defense is ListByStatusUnlabeled's own SQL exclusion (`id NOT
// IN (SELECT article_id FROM human_labels)`), which filters an
// already-labeled article out of the candidate list before the per-
// candidate loop ever runs -- so a normal, sequential (non-racing) run
// reports it as never-a-candidate, not as a skip. backfillrejects.go's
// errors.Is(err, training.ErrAlreadyLabeled)/skipped++ branch is
// unreachable from this in-process test for that same reason: it only
// fires under a real TOCTOU race (another writer labels the article
// between ListByStatusUnlabeled's SELECT and this command's own Label()
// call), which a sequential test cannot trigger without unsafe
// concurrent use of the shared singleton root command. That branch is
// intentional defense-in-depth for that race window, not dead code.
func TestBackfillRejectsCmd_NeverTouchesAlreadyLabeledArticleAndSecondRunIsIdempotent(t *testing.T) {
	configPath, _ := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: "reject-candidate-prelabeled", Title: "Owls sign a new punter",
		Status: article.StatusRejectedQuality, FetchedAt: time.Now().UTC(), Source: testSource(t),
	}); err != nil {
		db.Close()
		t.Fatalf("seed prelabeled candidate: %v", err)
	}
	if err := articles.Save(context.Background(), article.Article{
		ID: "reject-candidate-fresh", Title: "Hornets injury report",
		Status: article.StatusRejectedQuality, FetchedAt: time.Now().UTC(), Source: testSource(t),
	}); err != nil {
		db.Close()
		t.Fatalf("seed fresh candidate: %v", err)
	}
	if err := sqliteadapter.NewHumanLabelRepository(db).Label(context.Background(), training.Example{
		ArticleID: "reject-candidate-prelabeled", Label: "publish", LabeledAt: time.Now().UTC(), Source: "cli:label",
	}); err != nil {
		db.Close()
		t.Fatalf("seed pre-existing human label: %v", err)
	}
	db.Close()

	// First run: the pre-labeled article is excluded by
	// ListByStatusUnlabeled's SQL before the candidate loop even sees it,
	// so it is never a candidate at all -- 1 candidate found (the fresh
	// one), 1 labeled, 0 skipped. It is not counted as "skipped" because
	// skipped counts only the errors.Is(ErrAlreadyLabeled) race-recovery
	// path documented above, which this sequential run cannot hit.
	out, err := testsupport.Execute(t, "--config", configPath, "train", "backfill-rejects")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "1 candidate(s) found") || !strings.Contains(out, "1 labeled") || !strings.Contains(out, "0 skipped") {
		t.Errorf("output = %q, want it to report 1 candidate(s) found, 1 labeled, 0 skipped", out)
	}

	db, err = infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	labels := sqliteadapter.NewHumanLabelRepository(db)
	fresh, ok, err := labels.GetActive(context.Background(), "reject-candidate-fresh")
	if err != nil {
		db.Close()
		t.Fatalf("GetActive(reject-candidate-fresh) error = %v", err)
	}
	if !ok || fresh.Label != "reject" || fresh.Source != "pipeline_backfill" {
		db.Close()
		t.Errorf("GetActive(reject-candidate-fresh) = %+v, ok=%v, want label=reject source=pipeline_backfill", fresh, ok)
	}
	preexisting, ok, err := labels.GetActive(context.Background(), "reject-candidate-prelabeled")
	if err != nil {
		db.Close()
		t.Fatalf("GetActive(reject-candidate-prelabeled) error = %v", err)
	}
	if !ok || preexisting.Label != "publish" || preexisting.Source != "cli:label" {
		db.Close()
		t.Errorf("GetActive(reject-candidate-prelabeled) = %+v, ok=%v, want the original publish/cli:label untouched", preexisting, ok)
	}
	db.Close()

	// Idempotency: run it again -- both articles now have a human_labels
	// row, so ListByStatusUnlabeled returns zero candidates and nothing
	// is written.
	out2, err := testsupport.Execute(t, "--config", configPath, "train", "backfill-rejects")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if !strings.Contains(out2, "0 candidate(s) found") || !strings.Contains(out2, "0 labeled") {
		t.Errorf("second run output = %q, want it to report 0 candidate(s) found, 0 labeled (idempotent)", out2)
	}

	db, err = infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db after second run: %v", err)
	}
	defer db.Close()
	labels = sqliteadapter.NewHumanLabelRepository(db)
	fresh2, ok, err := labels.GetActive(context.Background(), "reject-candidate-fresh")
	if err != nil {
		t.Fatalf("GetActive(reject-candidate-fresh) after second run error = %v", err)
	}
	if !ok || fresh2.Revision != 1 {
		t.Errorf("GetActive(reject-candidate-fresh) after second run = %+v, ok=%v, want revision=1 (unchanged, not double-labeled)", fresh2, ok)
	}
}
