package trainwiring_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

var opsNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func opsFixture(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, cfg
}

func TestExcludedSourcesUnion(t *testing.T) {
	cfg := &config.Config{}
	cfg.Editorial.ModelBypassSourceIDs = []string{"rss:a", "rss:b"}
	cfg.Editorial.UnconditionalPublishSourceIDs = []string{"rss:b", "rss:c"}
	got := trainwiring.ExcludedSources(cfg)
	if len(got) != 3 || !got["rss:a"] || !got["rss:b"] || !got["rss:c"] {
		t.Errorf("ExcludedSources = %v, want a, b, c", got)
	}
}

func TestBuildDatasetNoExamples(t *testing.T) {
	db, cfg := opsFixture(t)
	cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.9
	_, err := trainwiring.BuildDataset(context.Background(), db, cfg,
		trainwiring.BuildFlags{OutPath: filepath.Join(t.TempDir(), "d.yml"), MaxPerCluster: 1}, opsNow)
	if err == nil || !strings.Contains(err.Error(), "no Gold or Silver examples found") {
		t.Fatalf("err = %v, want the no-examples error", err)
	}
}

func TestBuildDatasetRequiresAutoLabelThreshold(t *testing.T) {
	db, cfg := opsFixture(t)
	cfg.OfflineTraining.LLM.AutoLabelThreshold = 0
	_, err := trainwiring.BuildDataset(context.Background(), db, cfg, trainwiring.BuildFlags{OutPath: "x.yml"}, opsNow)
	if err == nil || !strings.Contains(err.Error(), "auto_label_threshold must be configured above 0") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildDatasetWritesManifest(t *testing.T) {
	db, cfg := opsFixture(t)
	cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.9
	ctx := context.Background()
	src := article.Article{
		ID: "gold1", OriginalURL: "https://example.com/g", CanonicalURL: "https://example.com/g",
		Source:    source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS},
		FetchedAt: opsNow, PublishedAt: opsNow, Status: article.StatusFetched, Title: "Team wins",
	}
	if err := sqliteadapter.NewArticleRepository(db).Save(ctx, src); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := sqliteadapter.NewHumanLabelRepository(db).Label(ctx, training.Example{ArticleID: "gold1", Label: "publish", LabeledAt: opsNow}); err != nil {
		t.Fatalf("label: %v", err)
	}
	out := filepath.Join(t.TempDir(), "dataset.yml")
	rep, err := trainwiring.BuildDataset(ctx, db, cfg, trainwiring.BuildFlags{OutPath: out, MaxPerCluster: 1}, opsNow)
	if err != nil {
		t.Fatalf("BuildDataset: %v", err)
	}
	if rep.OutPath != out || rep.Examples != 1 || rep.Gold != 1 || rep.Silver != 0 || rep.Dropped != 0 {
		t.Errorf("report = %+v", rep)
	}
	if _, err := os.Stat(out + ".manifest.json"); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}

func TestEvaluateBadCostGrid(t *testing.T) {
	db, cfg := opsFixture(t)
	dataset := filepath.Join(t.TempDir(), "d.yml")
	var b strings.Builder
	b.WriteString("examples:\n")
	for i := 0; i < 15; i++ {
		b.WriteString("  - article_id: p" + string(rune('a'+i)) + "\n    title: \"isotopes win big game number " + string(rune('a'+i)) + "\"\n    label: publish\n    published_at: 2026-01-01T00:00:00Z\n")
		b.WriteString("  - article_id: r" + string(rune('a'+i)) + "\n    title: \"sponsored shop discount deal number " + string(rune('a'+i)) + "\"\n    label: reject\n    published_at: 2026-01-01T00:00:00Z\n")
	}
	if err := os.WriteFile(dataset, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	_, err := trainwiring.Evaluate(context.Background(), db, cfg, trainwiring.EvaluateFlags{
		DatasetPath: dataset, OutDir: outDir, Algorithms: []string{"naive_bayes"}, Seed: 42,
		AcceptTargetPrecision: 0.7, RejectTargetPrecision: 0.7, NearDupThreshold: 0.8, CostGrid: "0.7,abc",
	}, opsNow)
	if err == nil || !strings.Contains(err.Error(), `train evaluate: invalid --cost-grid value "abc"`) {
		t.Fatalf("err = %v", err)
	}
	// The grid is validated before any candidate is trained or registered.
	if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
		t.Errorf("out dir has %d entries, want none", len(entries))
	}
}

func TestShadowCompareRejectsNonPositiveDays(t *testing.T) {
	db, cfg := opsFixture(t)
	_, err := trainwiring.ShadowCompare(context.Background(), db, cfg, "cand", 0, 10, opsNow)
	if err == nil || err.Error() != "train shadow-compare: --days must be positive" {
		t.Fatalf("err = %v", err)
	}
	cfg.Model.Path = ""
	_, err = trainwiring.ShadowCompare(context.Background(), db, cfg, "cand", 1, 10, opsNow)
	if err == nil || !strings.Contains(err.Error(), "model.path is not configured") {
		t.Fatalf("err = %v", err)
	}
}

// teacherLLM answers the first chat-completion call with labelsJSON and
// every later one with unparseable content (a failed batch).
func teacherLLM(t *testing.T, labelsJSON string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		content := "not json"
		if calls.Add(1) == 1 {
			content = labelsJSON
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClassifyMissingBaseURL(t *testing.T) {
	db, cfg := opsFixture(t)
	_, err := trainwiring.Classify(context.Background(), db, cfg, trainwiring.ClassifyFlags{}, opsNow)
	if err == nil || err.Error() != "offline_training.llm.base_url is required (or pass --llm-base-url)" {
		t.Fatalf("err = %v", err)
	}
	_, err = trainwiring.Classify(context.Background(), db, cfg, trainwiring.ClassifyFlags{BaseURL: "http://127.0.0.1:1"}, opsNow)
	if err == nil || err.Error() != "offline_training.llm.model is required (or pass --llm-model)" {
		t.Fatalf("err = %v", err)
	}
	_, err = trainwiring.Classify(context.Background(), db, cfg, trainwiring.ClassifyFlags{BaseURL: "http://127.0.0.1:1", Model: "m"}, opsNow)
	if err == nil || err.Error() != "offline_training.llm.timeout_seconds must be positive" {
		t.Fatalf("err = %v", err)
	}
}

func TestClassifyFailedBatchesNotAnError(t *testing.T) {
	db, cfg := opsFixture(t)
	cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	// BatchSize 0 in the flags means "configured value", so this run
	// sends one article per request.
	cfg.OfflineTraining.LLM.BatchSize = 1
	ctx := context.Background()
	for i, id := range []string{"t1", "t2"} {
		at := opsNow.Add(time.Duration(i) * time.Minute)
		if err := sqliteadapter.NewArticleRepository(db).Save(ctx, article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source:    source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS},
			FetchedAt: at, PublishedAt: at, Status: article.StatusFetched, Title: "Story " + id,
		}); err != nil {
			t.Fatalf("seed article: %v", err)
		}
	}
	srv := teacherLLM(t, `{"labels": [{"article_id": "t1", "label": "publish", "confidence": 0.9, "reason": "on topic"}]}`)

	rep, err := trainwiring.Classify(ctx, db, cfg, trainwiring.ClassifyFlags{BaseURL: srv.URL, Model: "m"}, opsNow)
	if err != nil {
		t.Fatalf("Classify: %v, want failed batches reported, not returned", err)
	}
	r := rep.Result
	if r.ArticlesConsidered != 2 || r.BatchesTotal != 2 || r.BatchesFailed != 1 || r.ProposalsSaved != 1 || len(r.BatchErrors) != 1 {
		t.Fatalf("result = %+v, want 1 of 2 batches failed and the other's proposal saved", r)
	}
	// The fixture's editorial.mode is disabled, so the queue sync is skipped.
	if rep.SyncSkipped != "editorial.mode is disabled (no promoted editorial model expected)" || rep.Sync != nil {
		t.Errorf("SyncSkipped = %q Sync = %v", rep.SyncSkipped, rep.Sync)
	}
}

func TestClassifyEvalNoExamples(t *testing.T) {
	_, cfg := opsFixture(t)
	cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	dataset := filepath.Join(t.TempDir(), "d.yml")
	// The only example is an imported label, excluded by default.
	if err := os.WriteFile(dataset, []byte("examples:\n  - article_id: x1\n    title: t\n    label: publish\n    label_source: telegramimport\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := trainwiring.ClassifyEvalFlags{DatasetPath: dataset, Fold: "all", BaseURL: "http://127.0.0.1:1", Model: "m"}
	rep, err := trainwiring.ClassifyEval(context.Background(), cfg, f)
	if err == nil || err.Error() != "no examples selected from "+dataset {
		t.Fatalf("err = %v", err)
	}
	if rep.Examples != 0 {
		t.Errorf("Examples = %d, want 0", rep.Examples)
	}

	f.Model = ""
	cfg.OfflineTraining.LLM.Model = ""
	_, err = trainwiring.ClassifyEval(context.Background(), cfg, f)
	if err == nil || err.Error() != "offline_training.llm base_url, model and timeout_seconds are required" {
		t.Fatalf("err = %v", err)
	}
}

func TestClassifyEvalMissesWriteErrorKeepsReport(t *testing.T) {
	_, cfg := opsFixture(t)
	cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	dataset := filepath.Join(t.TempDir(), "d.yml")
	if err := os.WriteFile(dataset, []byte("examples:\n  - article_id: x1\n    title: t\n    label: publish\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := teacherLLM(t, `{"labels": [{"article_id": "x1", "label": "discard", "confidence": 0.99, "reason": "r"}]}`)
	f := trainwiring.ClassifyEvalFlags{
		DatasetPath: dataset, Fold: "all", BaseURL: srv.URL, Model: "m", Threshold: 0.9,
		MissesOut: filepath.Join(t.TempDir(), "missing-dir", "misses.csv"),
	}
	rep, err := trainwiring.ClassifyEval(context.Background(), cfg, f)
	if !errors.Is(err, trainwiring.ErrWriteMisses) {
		t.Fatalf("err = %v, want ErrWriteMisses", err)
	}
	if rep.Examples != 1 || rep.Fold != "all" || rep.Model != "m" || rep.Summary.PublishDiscarded != 1 || rep.MissesWritten != "" {
		t.Errorf("report = %+v, want the evaluated report without MissesWritten", rep)
	}
}

func TestClassifyEvalZeroBatchSizeUsesConfigured(t *testing.T) {
	_, cfg := opsFixture(t)
	cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	cfg.OfflineTraining.LLM.BatchSize = 1
	dataset := filepath.Join(t.TempDir(), "d.yml")
	if err := os.WriteFile(dataset, []byte("examples:\n  - article_id: x1\n    title: t\n    label: publish\n  - article_id: x2\n    title: u\n    label: reject\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := teacherLLM(t, `{"labels": [{"article_id": "x1", "label": "publish", "confidence": 0.9, "reason": "r"}]}`)
	rep, err := trainwiring.ClassifyEval(context.Background(), cfg, trainwiring.ClassifyEvalFlags{
		DatasetPath: dataset, Fold: "all", BaseURL: srv.URL, Model: "m", Threshold: 0.9,
	})
	if err != nil {
		t.Fatalf("ClassifyEval: %v", err)
	}
	// offline_training.llm.batch_size 1 wins over the default 20: one
	// request per example.
	if rep.Report.BatchesTotal != 2 {
		t.Errorf("BatchesTotal = %d, want 2 (configured batch size 1)", rep.Report.BatchesTotal)
	}
}
