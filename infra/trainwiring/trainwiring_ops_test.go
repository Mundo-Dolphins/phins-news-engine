package trainwiring_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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
		FetchedAt: opsNow, PublishedAt: opsNow, Status: article.StatusFetched, Title: "Dolphins win",
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
