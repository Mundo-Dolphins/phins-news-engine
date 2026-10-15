package train_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

// TestTrainRunWorkflowCmd_RequiresConfiguredModelPath also pins the
// command's registration: an unregistered `train run` would fail with
// cobra's "unknown command" instead of the model.path guard.
func TestTrainRunWorkflowCmd_RequiresConfiguredModelPath(t *testing.T) {
	configPath := testsupport.WriteFixture(t) // model.path is "" in the base fixture

	_, err := testsupport.Execute(t, "--config", configPath, "train", "run")
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when model.path is unconfigured")
	}
	if !strings.Contains(err.Error(), "model.path") {
		t.Errorf("Execute() error = %q, want it to mention model.path", err.Error())
	}
}

// seedGoldLabels stores n publish and n reject articles with human
// labels, so `train run`'s build stage has a real Gold dataset to export
// from the operational database.
func seedGoldLabels(t *testing.T, dbPath string, n int) {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	articles := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "run-source"), Name: "Run Source", Type: source.TypeRSS}

	for i := 0; i < n; i++ {
		for _, spec := range []struct{ prefix, title, label string }{
			{"p", "isotopes win big game article number ", "publish"},
			{"r", "sponsored shop now huge discount deal number ", "reject"},
		} {
			id := fmt.Sprintf("%s%d", spec.prefix, i)
			at := base.AddDate(0, 0, i)
			if err := articles.Save(ctx, article.Article{
				ID: id, Title: spec.title + fmt.Sprint(i),
				OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
				Source: src, Domain: "example.com",
				FetchedAt: at, PublishedAt: at, Status: article.StatusFetched,
			}); err != nil {
				t.Fatalf("seed article %s: %v", id, err)
			}
			if err := labels.Label(ctx, training.Example{ArticleID: id, Label: spec.label, LabeledAt: at}); err != nil {
				t.Fatalf("label article %s: %v", id, err)
			}
		}
	}
}

func TestTrainRunWorkflowCmd_RunsEndToEndAndPersistsTheRun(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	dbPath := filepath.Join(dir, "operational.db")
	seedGoldLabels(t, dbPath, 15)

	modelPath := filepath.Join(dir, "models", "editorial", "current")
	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, modelPath)

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "run",
		"--dataset", filepath.Join(t.TempDir(), "dataset.yml"),
		"--candidates", filepath.Join(t.TempDir(), "candidates"))
	if err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}
	if !strings.Contains(out, "Recommendation: PROMOTE") {
		t.Errorf("output = %q, want the cleanly-separable seeded dataset to clear the fixture's gates and be promoted", out)
	}
	if !strings.Contains(out, "Dataset: 30 examples (30 Gold, 0 Silver)") {
		t.Errorf("output = %q, want it to report the seeded 30-example Gold dataset", out)
	}
	if _, err := editorialmodel.Load(modelPath); err != nil {
		t.Errorf("editorialmodel.Load(%s) error = %v, want the promoted model written to model.path", modelPath, err)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	latest, err := sqliteadapter.NewTrainingRunRepository(db).Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if latest == nil {
		t.Fatal("Latest() = nil, want the persisted training run row")
	}
	if latest.Status != "succeeded" {
		t.Errorf("Latest().Status = %q, want \"succeeded\"", latest.Status)
	}
	if latest.Report != out {
		t.Errorf("persisted report differs from printed output:\npersisted = %q\nprinted   = %q", latest.Report, out)
	}
}

// rewriteFixtureAutoEnabled inserts a retrain_triggers block with
// auto_enabled: true and the given min_new_gold_examples threshold
// right before the offline_training.llm block, following
// rewriteFixtureZeroAutoLabelThreshold's existing string-splice
// pattern in train_test.go.
func rewriteFixtureAutoEnabled(t *testing.T, srcConfigPath, dstConfigPath string, minNewGoldExamples int) {
	t.Helper()
	data, err := os.ReadFile(srcConfigPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	anchor := "offline_training:\n  llm:\n"
	block := strings.Replace(anchor, "  llm:\n", strings.Join([]string{
		"  retrain_triggers:\n",
		"    auto_enabled: true\n",
		"    min_new_gold_examples: " + strconv.Itoa(minNewGoldExamples) + "\n",
		"  llm:\n",
	}, ""), 1)
	rewritten := strings.Replace(string(data), anchor, block, 1)
	if rewritten == string(data) {
		t.Fatal("rewriteFixtureAutoEnabled: offline_training/llm anchor not found in fixture — check testsupport.FixtureTemplate hasn't changed shape")
	}
	if err := os.WriteFile(dstConfigPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write rewritten fixture: %v", err)
	}
}

func TestTrainRunCmd_AutoDisabledIsNoOp(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	if err := labels.Label(context.Background(), training.Example{ArticleID: articleID, Label: "publish", LabeledAt: time.Now()}); err != nil {
		t.Fatalf("save label: %v", err)
	}

	// The base fixture's model.path is "" (see testsupport.FixtureTemplate),
	// and train run checks model.path before its --auto gate, so every
	// --auto scenario below needs model.path configured to reach that
	// gate at all -- RewriteFixtureModelPath is the existing helper for
	// that (see TestTrainRunWorkflowCmd_RunsEndToEndAndPersistsTheRun
	// above).
	dir := filepath.Dir(configPath)
	modelPath := filepath.Join(dir, "models", "editorial", "current")
	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, modelPath)

	// fixture's default offline_training has no retrain_triggers block
	// at all -- AutoEnabled's Go zero value (false) applies.
	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "run", "--auto",
		"--dataset", filepath.Join(t.TempDir(), "dataset.yml"), "--candidates", t.TempDir())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "auto training disabled") {
		t.Errorf("output = %q, want it to report auto training disabled", out)
	}

	runRepo := sqliteadapter.NewTrainingRunRepository(db)
	latest, err := runRepo.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if latest != nil {
		t.Errorf("Latest() = %+v, want nil -- a disabled --auto run must write no training_runs row", latest)
	}
}

func TestTrainRunCmd_AutoEnabledButNotTriggeredIsNoOp(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	if err := labels.Label(context.Background(), training.Example{ArticleID: articleID, Label: "publish", LabeledAt: time.Now()}); err != nil {
		t.Fatalf("save label: %v", err)
	}

	dir := filepath.Dir(configPath)
	modelPath := filepath.Join(dir, "models", "editorial", "current")
	withModelPath := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, withModelPath, modelPath)

	overriddenConfig := filepath.Join(dir, "settings-auto-enabled-high-threshold.yml")
	// min_new_gold_examples: 1000 -- the single seeded label never
	// reaches it, so this exercises "enabled but nothing fired".
	rewriteFixtureAutoEnabled(t, withModelPath, overriddenConfig, 1000)

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "run", "--auto",
		"--dataset", filepath.Join(t.TempDir(), "dataset.yml"), "--candidates", t.TempDir())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "no retrain triggered") {
		t.Errorf("output = %q, want it to report no retrain triggered", out)
	}

	runRepo := sqliteadapter.NewTrainingRunRepository(db)
	latest, err := runRepo.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if latest != nil {
		t.Errorf("Latest() = %+v, want nil -- a not-triggered --auto run must write no training_runs row", latest)
	}
}

func TestTrainRunCmd_AutoTriggeredRunsFullPipeline(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	if err := labels.Label(context.Background(), training.Example{ArticleID: articleID, Label: "publish", LabeledAt: time.Now()}); err != nil {
		t.Fatalf("save label: %v", err)
	}

	dir := filepath.Dir(configPath)
	modelPath := filepath.Join(dir, "models", "editorial", "current")
	withModelPath := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, withModelPath, modelPath)

	overriddenConfig := filepath.Join(dir, "settings-auto-enabled-low-threshold.yml")
	// min_new_gold_examples: 1 -- the single seeded label meets it.
	rewriteFixtureAutoEnabled(t, withModelPath, overriddenConfig, 1)

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "run", "--auto",
		"--dataset", filepath.Join(t.TempDir(), "dataset.yml"), "--candidates", t.TempDir())
	// A single publish-only label can't produce a usable train/validate/
	// test split (editorialtrain.Split requires both classes), so this
	// specific fixture is expected to fail at the evaluate stage -- what
	// this test actually verifies is that the run was NOT skipped as
	// "no retrain triggered" (i.e. GatherAutoSignals + Evaluate found
	// the 1-example threshold met and proceeded into the real
	// pipeline), which the error message below confirms.
	if err == nil {
		t.Fatal("Execute() error = nil, want a build/evaluate-stage error from the single-class fixture (confirms the pipeline actually ran)")
	}
	if strings.Contains(out, "no retrain triggered") || strings.Contains(out, "auto training disabled") {
		t.Errorf("output = %q, must not report a no-op when the threshold is met", out)
	}

	runRepo := sqliteadapter.NewTrainingRunRepository(db)
	latest, err := runRepo.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if latest == nil || latest.Status != "failed" {
		t.Errorf("Latest() = %+v, want a persisted row with Status=\"failed\" (the pipeline ran and its evaluate stage failed)", latest)
	}
}
