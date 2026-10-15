package train_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func activeLabels(t *testing.T, dbPath string) map[string]string {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()
	examples, err := sqliteadapter.NewHumanLabelRepository(db).ListEditorialExamples(context.Background())
	if err != nil {
		t.Fatalf("ListEditorialExamples: %v", err)
	}
	out := map[string]string{}
	for _, e := range examples {
		out[e.ArticleID] = e.Label + "/" + e.Source
	}
	return out
}

func TestImportLabels_FromDBIsAdditiveAndNeverOverrides(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	archive := filepath.Join(t.TempDir(), "archive.db")
	testsupport.CopyMigratedTestDB(t, archive)

	seed := func(path string, e training.EditorialExample) {
		db, err := infrasqlite.Open(path, 5*time.Second)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		defer db.Close()
		if _, err := sqliteadapter.NewHumanLabelRepository(db).ImportEditorialExample(context.Background(), e, false); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	seed(archive, training.EditorialExample{ArticleID: "old-reject", Label: "reject", Source: "cli:label", Title: "chatter", LabeledAt: now, FetchedAt: now})
	seed(archive, training.EditorialExample{ArticleID: "shared", Label: "reject", Source: "cli:label", Title: "x", LabeledAt: now, FetchedAt: now})
	seed(dbPath, training.EditorialExample{ArticleID: "shared", Label: "publish", Source: "telegramimport", Title: "x", LabeledAt: now, FetchedAt: now})

	out, err := testsupport.Execute(t, "train", "import-labels", "--from-db", archive, "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}
	got := activeLabels(t, dbPath)
	if got["old-reject"] != "reject/cli:label" {
		t.Errorf("old-reject = %q, want the archived reject imported", got["old-reject"])
	}
	if got["shared"] != "publish/telegramimport" {
		t.Errorf("shared = %q, want the existing label kept (archive imports never override)", got["shared"])
	}
	if !strings.Contains(out, "1 inserted") || !strings.Contains(out, "1 conflict") {
		t.Errorf("output = %q, want a summary with 1 inserted and 1 conflict", out)
	}
}

func TestImportLabels_FromDatasetLetsReviewDecisionsOverride(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	now := time.Date(2026, 9, 29, 13, 0, 0, 0, time.UTC)
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	repo := sqliteadapter.NewHumanLabelRepository(db)
	for _, id := range []string{"reviewed", "not-reviewed"} {
		if _, err := repo.ImportEditorialExample(context.Background(), training.EditorialExample{ArticleID: id, Label: "publish", Source: "telegramimport", Title: id, LabeledAt: now, FetchedAt: now}, false); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	db.Close()

	datasetPath := filepath.Join(t.TempDir(), "reviewed.yml")
	if err := editorialtrain.SaveDataset(datasetPath, []editorialtrain.TrainingExample{
		{ArticleID: "reviewed", Title: "reviewed", Label: "reject", LabelSource: "interactive_review", Reason: "interactive_dataset_review", LabeledAt: now, FetchedAt: now},
		{ArticleID: "not-reviewed", Title: "not-reviewed", Label: "reject", LabelSource: "cli:label", LabeledAt: now, FetchedAt: now},
		{ArticleID: "new", Title: "new", Label: "reject", LabelSource: "interactive_review", LabeledAt: now, FetchedAt: now},
	}); err != nil {
		t.Fatalf("SaveDataset: %v", err)
	}

	out, err := testsupport.Execute(t, "train", "import-labels", "--from-dataset", datasetPath, "--override-label-source", "interactive_review", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}
	got := activeLabels(t, dbPath)
	want := map[string]string{
		"reviewed":     "reject/interactive_review",
		"not-reviewed": "publish/telegramimport",
		"new":          "reject/interactive_review",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %q, want %q", id, got[id], w)
		}
	}
}

func TestImportLabels_RequiresASource(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	if _, err := testsupport.Execute(t, "train", "import-labels", "--config", configPath); err == nil {
		t.Fatal("Execute() error = nil, want an error when neither --from-db nor --from-dataset is given")
	}
}
