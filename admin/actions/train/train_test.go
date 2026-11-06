package train_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/train"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t         *testing.T
	db        *sql.DB
	cfg       *config.Config
	modelPath string
	log       *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	modelPath := filepath.Join(dir, "models", "editorial", "current")
	overridden := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overridden, modelPath)
	cfg, err := config.Load(overridden)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return fixture{t: t, db: db, cfg: cfg, modelPath: modelPath, log: &bytes.Buffer{}}
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range train.Actions() {
		if c.Name == name {
			a = c
		}
	}
	if a.Name == "" {
		f.t.Fatalf("no action %q", name)
	}
	p, err := action.Decode(a, json.RawMessage(params))
	if err != nil {
		return action.Result{}, err
	}
	env := action.Env{DB: f.db, Config: f.cfg, Mode: action.ModeLocal, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: "user:ana"}
	return a.Run(context.Background(), env, p)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestMetadata(t *testing.T) {
	type want struct {
		kind        action.Kind
		modes       int
		longRunning bool
		exclusive   bool
		title       string
	}
	cases := map[string]want{
		"train.analyze":          {action.KindRead, 2, false, false, "Analyze training signals"},
		"train.run":              {action.KindDestructive, 1, true, true, "Run training pipeline"},
		"train.promote":          {action.KindDestructive, 1, false, true, "Promote candidate model"},
		"train.rollback":         {action.KindDestructive, 1, false, true, "Roll back model"},
		"train.build":            {action.KindWrite, 1, false, false, "Build training dataset"},
		"train.evaluate":         {action.KindWrite, 1, true, true, "Evaluate candidate models"},
		"train.mine":             {action.KindWrite, 1, true, true, "Mine hard examples"},
		"train.shadow_compare":   {action.KindWrite, 1, true, false, "Shadow-compare candidate model"},
		"train.audit_dataset":    {action.KindWrite, 1, false, false, "Audit dataset"},
		"train.audit_gates":      {action.KindWrite, 1, false, false, "Audit dataset against the gates"},
		"train.recheck_corpus":   {action.KindRead, 2, false, false, "Recheck corpus against the dictionary"},
		"train.backfill_rejects": {action.KindWrite, 1, false, true, "Backfill reject labels"},
		"train.import_labels":    {action.KindWrite, 1, false, true, "Import labels"},
	}
	as := train.Actions()
	if len(as) != len(cases) {
		t.Fatalf("got %d actions, want %d", len(as), len(cases))
	}
	for _, a := range as {
		w, ok := cases[a.Name]
		if !ok {
			t.Fatalf("unexpected action %s", a.Name)
		}
		if a.Group != "train" || a.Kind != w.kind || len(a.Modes) != w.modes || a.LongRunning != w.longRunning || a.Exclusive != w.exclusive || a.Title != w.title {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		if w.modes == 1 && a.Modes[0] != action.ModeLocal {
			t.Errorf("%s must be local-only", a.Name)
		}
		if a.Description == "" {
			t.Errorf("%s has no description", a.Name)
		}
	}
}

func TestAnalyze(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if _, err := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(f.db).Save(ctx, editoriallabelproposal.Proposal{
		ArticleID: "a1", Label: "publish", Confidence: 0.99, Model: "m", PromptVersion: "p1", PolicyVersion: "pol1", CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	res, err := f.run("train.analyze", `{}`)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.Render != "object" {
		t.Fatalf("render = %q", res.Render)
	}
	obj, ok := res.Object.(map[string]any)
	if !ok {
		t.Fatalf("object = %T", res.Object)
	}
	if obj["proposed"] != 1 {
		t.Errorf("proposed = %v, want 1", obj["proposed"])
	}
	for _, k := range []string{"auto_labeled", "sent_to_review", "queue_size", "queue_disagreement", "queue_novel_vocabulary", "queue_other"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
}

func failingCandidate(t *testing.T) string {
	t.Helper()
	failing := editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 99, FN: 5, TN: 900}, PRAUC: 0.1}
	data, err := json.Marshal(struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
	}{failing, failing})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, string(data), schema); err != nil {
		t.Fatal(err)
	}
	return dir
}

func params(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPromoteGateFailureIsConflictAndModelUntouched(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("train.promote", params(t, map[string]any{"candidate_dir": failingCandidate(t), "version": "v1.0.0"}))
	wantCode(t, err, action.CodeConflict)
	if _, loadErr := editorialmodel.Load(f.modelPath); loadErr == nil {
		t.Error("model dir was written despite gate failure")
	}
}

func TestPromoteForceAndSuccess(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("train.promote", params(t, map[string]any{"candidate_dir": failingCandidate(t), "version": "v1.0.0", "force": true}))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	obj, ok := res.Object.(map[string]any)
	if res.Render != "object" || !ok {
		t.Fatalf("result = %+v", res)
	}
	if obj["version"] != "v1.0.0" || obj["forced"] != true {
		t.Errorf("object = %v", obj)
	}
	if gf, _ := obj["gate_failures"].([]string); len(gf) == 0 {
		t.Errorf("gate_failures = %v", obj["gate_failures"])
	}
	if !strings.Contains(res.Summary, "promoted v1.0.0 to "+f.modelPath) || !strings.Contains(res.Summary, "make deploy") {
		t.Errorf("summary = %q", res.Summary)
	}
	if m, err := editorialmodel.Load(f.modelPath); err != nil || m.Version != "v1.0.0" {
		t.Errorf("model = %+v, %v", m, err)
	}
}

func TestPromoteNonNaiveBayesIsConflict(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "logistic_regression", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "logistic_regression", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, testsupport.PassingGateReportJSON(), schema); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("train.promote", params(t, map[string]any{"candidate_dir": dir}))
	wantCode(t, err, action.CodeConflict)
}

func TestPromoteRequiresCandidateDir(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("train.promote", `{}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestRollback(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run("train.promote", params(t, map[string]any{"candidate_dir": testsupport.WriteGatePassingCandidateModel(t), "version": "v1.0.0"})); err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	if _, err := f.run("train.rollback", `{}`); err == nil {
		t.Fatal("rollback with no previous slot succeeded")
	}
	if _, err := f.run("train.promote", params(t, map[string]any{"candidate_dir": testsupport.WriteGatePassingCandidateModel(t), "version": "v2.0.0"})); err != nil {
		t.Fatalf("promote v2: %v", err)
	}
	res, err := f.run("train.rollback", `{}`)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if res.Render != "text" || !strings.Contains(res.Text, "rolled back "+f.modelPath) {
		t.Errorf("result = %+v", res)
	}
	if m, err := editorialmodel.Load(f.modelPath); err != nil || m.Version != "v1.0.0" {
		t.Errorf("model = %+v, %v", m, err)
	}
}

func TestRunAutoDisabled(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.9
	f.cfg.OfflineTraining.RetrainTriggers.AutoEnabled = false
	res, err := f.run("train.run", `{"auto": true}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Render != "text" || !strings.Contains(res.Text, "auto training disabled (offline_training.retrain_triggers.auto_enabled=false)") {
		t.Errorf("result = %+v", res)
	}
}

func TestRunRejectsMisconfiguration(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.AutoLabelThreshold = 0
	_, err := f.run("train.run", `{}`)
	wantCode(t, err, action.CodeConflict)
	f.cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.9
	f.cfg.Model.Path = ""
	_, err = f.run("train.run", `{}`)
	wantCode(t, err, action.CodeConflict)
}

func TestRunDefaults(t *testing.T) {
	var a action.Action
	for _, c := range train.Actions() {
		if c.Name == "train.run" {
			a = c
		}
	}
	p, err := action.Decode(a, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	for _, want := range []string{`"dataset":"data/editorial_dataset.yml"`, `"candidates":"models/editorial/candidates"`, `"max_per_cluster":1`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("decoded %s missing %s", b, want)
		}
	}
}

func TestTrainBuildLocalOnly(t *testing.T) {
	var r action.Registry
	r.Add(train.Actions()...)
	for _, a := range r.For(action.ModeProduction) {
		switch a.Name {
		case "train.build", "train.evaluate", "train.mine", "train.shadow_compare":
			t.Errorf("%s is available in production mode", a.Name)
		}
	}
	for _, name := range []string{"train.build", "train.evaluate", "train.mine", "train.shadow_compare"} {
		if _, ok := r.Get(name); !ok {
			t.Errorf("%s not registered", name)
		}
	}
}

func TestTrainEvaluateIsExclusiveJob(t *testing.T) {
	var r action.Registry
	r.Add(train.Actions()...)
	a, ok := r.Get("train.evaluate")
	if !ok || !a.LongRunning || !a.Exclusive {
		t.Fatalf("train.evaluate = %+v, want LongRunning and Exclusive", a)
	}
}

func TestBuildNeedsAutoLabelThreshold(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.AutoLabelThreshold = 0
	_, err := f.run("train.build", `{}`)
	wantCode(t, err, action.CodeConflict)
}

func TestBuildNoExamplesIsError(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.9
	_, err := f.run("train.build", params(t, map[string]any{"out": filepath.Join(t.TempDir(), "d.yml")}))
	if err == nil || !strings.Contains(err.Error(), "no Gold or Silver examples") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildWritesDataset(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.9
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
		ID: "g1", OriginalURL: "https://example.com/g1", CanonicalURL: "https://example.com/g1",
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusFetched, Title: "Team wins",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.NewHumanLabelRepository(f.db).Label(ctx, training.Example{ArticleID: "g1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "d.yml")
	res, err := f.run("train.build", params(t, map[string]any{"out": out}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := "built 1 examples to " + out + " (1 gold, 0 silver; 0 dropped by --max-per-cluster)"
	if res.Render != "text" || !strings.Contains(res.Text, want) {
		t.Errorf("result = %+v, want text containing %q", res, want)
	}
}

func TestEvaluateRequiresDataset(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("train.evaluate", `{}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestEvaluateRendersTable(t *testing.T) {
	f := newFixture(t)
	var b strings.Builder
	b.WriteString("examples:\n")
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&b, "  - article_id: p%d\n    title: \"isotopes win big game article number %d\"\n    label: publish\n    published_at: 2026-01-01T00:00:00Z\n", i, i)
		fmt.Fprintf(&b, "  - article_id: r%d\n    title: \"sponsored shop now huge discount deal number %d\"\n    label: reject\n    published_at: 2026-01-01T00:00:00Z\n", i, i)
	}
	dataset := filepath.Join(t.TempDir(), "d.yml")
	if err := os.WriteFile(dataset, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	res, err := f.run("train.evaluate", params(t, map[string]any{
		"dataset": dataset, "out": outDir, "algorithm": []string{"naive_bayes"},
		"accept_target_precision": 0.7, "reject_target_precision": 0.7, "cost_grid": "0.7,0.9",
	}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Render != "table" || len(res.Columns) != 9 || len(res.Rows) != 1 || res.Rows[0]["algorithm"] != "naive_bayes" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Summary, "cost-grid") || !strings.Contains(res.Summary, "candidates written under "+outDir) {
		t.Errorf("summary = %q", res.Summary)
	}
	if _, err := editorialmodel.Load(filepath.Join(outDir, "naive_bayes")); err != nil {
		t.Errorf("candidate not written: %v", err)
	}
}

func TestMineSkipsWithoutPromotedModel(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("train.mine", `{}`)
	if err != nil {
		t.Fatalf("mine: %v", err)
	}
	if res.Render != "text" || res.Text == "" {
		t.Errorf("result = %+v", res)
	}
}

func TestShadowCompareValidation(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("train.shadow_compare", `{}`)
	wantCode(t, err, action.CodeInvalidParams)
	_, err = f.run("train.shadow_compare", params(t, map[string]any{"candidate": "x", "days": 0}))
	if err == nil || !strings.Contains(err.Error(), "--days must be positive") {
		t.Fatalf("err = %v", err)
	}
	f.cfg.Model.Path = ""
	_, err = f.run("train.shadow_compare", params(t, map[string]any{"candidate": "x"}))
	wantCode(t, err, action.CodeConflict)
}

func TestImportLabelsNeedsASource(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("train.import_labels", `{}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestImportLabelsFromDatasetRendersTally(t *testing.T) {
	f := newFixture(t)
	ds := filepath.Join(t.TempDir(), "ds.yml")
	if err := editorialtrain.SaveDataset(ds, []editorialtrain.TrainingExample{
		{ArticleID: "x", Label: "publish", LabelSource: "interactive_review", Title: "t", LabeledAt: now, FetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("train.import_labels", params(t, map[string]any{"from_dataset": []string{ds}}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "table" || len(res.Rows) != 1 || res.Rows[0]["inserted"] != 1 || res.Rows[0]["path"] != ds {
		t.Errorf("result = %+v", res)
	}
}

func TestBackfillRejectsDryRun(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("train.backfill_rejects", `{"dry_run": true}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "backfill-rejects: 0 candidate(s) found, 0 labeled, 0 skipped (already labeled) (dry-run=true)"
	if res.Text != want || res.Summary != want {
		t.Errorf("result = %+v", res)
	}
}

func TestRecheckCorpusAvailableInProduction(t *testing.T) {
	var r action.Registry
	r.Add(train.Actions()...)
	found := false
	for _, a := range r.For(action.ModeProduction) {
		switch a.Name {
		case "train.recheck_corpus":
			found = true
		case "train.audit_dataset", "train.audit_gates", "train.backfill_rejects", "train.import_labels":
			t.Errorf("%s is available in production mode", a.Name)
		}
	}
	if !found {
		t.Fatal("train.recheck_corpus must be available in production")
	}

	f := newFixture(t)
	_, dictPath := testsupport.WriteFixtureWithDictionary(t)
	repo := sqliteadapter.NewHumanLabelRepository(f.db)
	for id, title := range map[string]string{"good": "Springfield Isotopes sign new linebacker", "stale": "Local weather forecast for the weekend"} {
		if _, err := repo.ImportEditorialExample(context.Background(), training.EditorialExample{
			ArticleID: id, Label: "publish", Source: "cli:label", Title: title, LabeledAt: now, FetchedAt: now}, false); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (n int) {
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM editorial_examples`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	res, err := f.run("train.recheck_corpus", params(t, map[string]any{"dictionary": dictPath}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "2 examined, 1 flagged" || len(res.Rows) != 1 || res.Rows[0]["article_id"] != "stale" {
		t.Errorf("result = %+v", res)
	}
	if count() != before {
		t.Error("recheck wrote to editorial_examples")
	}
}

func TestAuditDatasetWritesCSVAndSummary(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	ds, out := filepath.Join(dir, "ds.yml"), filepath.Join(dir, "audit.csv")
	if err := editorialtrain.SaveDataset(ds, []editorialtrain.TrainingExample{
		{ArticleID: "r2", Title: "Weak reject", Label: "reject", LabelSource: "pipeline_backfill", SourceID: "rss:c"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("train.audit_dataset", params(t, map[string]any{"dataset": ds, "out": out}))
	if err != nil {
		t.Fatal(err)
	}
	want := "audited 1 eligible dataset row(s), excluding 0 official-source row(s); 1 candidate(s) written to " + out
	if res.Summary != want || len(res.Rows) != 1 || res.Rows[0]["article_id"] != "r2" {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("csv not written: %v", err)
	}
}

func TestBuildNoExamplesIsConflict(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.9
	_, err := f.run("train.build", params(t, map[string]any{"out": filepath.Join(t.TempDir(), "d.yml")}))
	wantCode(t, err, action.CodeConflict)
}
