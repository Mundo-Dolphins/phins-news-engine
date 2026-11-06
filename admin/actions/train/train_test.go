package train_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
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
		"train.analyze":  {action.KindRead, 2, false, false, "Analyze training signals"},
		"train.run":      {action.KindDestructive, 1, true, true, "Run training pipeline"},
		"train.promote":  {action.KindDestructive, 1, false, true, "Promote candidate model"},
		"train.rollback": {action.KindDestructive, 1, false, true, "Roll back model"},
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
