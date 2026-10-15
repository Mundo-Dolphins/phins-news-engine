package train_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func TestTrainExportCmd_WritesDatasetFromHumanLabels(t *testing.T) {
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

	outPath := filepath.Join(t.TempDir(), "dataset.yml")
	out, err := testsupport.Execute(t, "--config", configPath, "train", "build", "--out", outPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "1") {
		t.Errorf("output = %q, want it to mention the exported count (1)", out)
	}

	dataset, err := editorialtrain.LoadDataset(outPath)
	if err != nil {
		t.Fatalf("LoadDataset(%s) error = %v", outPath, err)
	}
	if len(dataset.Examples) != 1 || dataset.Examples[0].ArticleID != articleID || dataset.Examples[0].Label != "publish" {
		t.Errorf("dataset.Examples = %+v, want one publish example for %s", dataset.Examples, articleID)
	}
}

// rewriteFixtureZeroAutoLabelThreshold rewrites the fixture's
// offline_training.llm.auto_label_threshold down to its Go zero value
// (0), simulating an operator config that never configured it -- the
// scenario `train export`'s auto_label_threshold guard defends
// against (see FixtureTemplate, which otherwise bakes in 0.95 for
// every other test in this package).
func rewriteFixtureZeroAutoLabelThreshold(t *testing.T, srcConfigPath, dstConfigPath string) {
	t.Helper()
	data, err := os.ReadFile(srcConfigPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rewritten := strings.Replace(string(data), "auto_label_threshold: 0.95\n", "auto_label_threshold: 0\n", 1)
	if rewritten == string(data) {
		t.Fatal("rewriteFixtureZeroAutoLabelThreshold: auto_label_threshold line not found in fixture — check testsupport.FixtureTemplate hasn't changed shape")
	}
	if err := os.WriteFile(dstConfigPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write rewritten fixture: %v", err)
	}
}

func TestTrainExportCmd_UnconfiguredAutoLabelThresholdReturnsError(t *testing.T) {
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
	overriddenConfig := filepath.Join(dir, "settings-zero-auto-label-threshold.yml")
	rewriteFixtureZeroAutoLabelThreshold(t, configPath, overriddenConfig)

	_, err = testsupport.Execute(t, "--config", overriddenConfig, "train", "build", "--out", filepath.Join(t.TempDir(), "dataset.yml"))
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when offline_training.llm.auto_label_threshold is unconfigured (0)")
	}
	if !strings.Contains(err.Error(), "auto_label_threshold") {
		t.Errorf("Execute() error = %q, want it to mention auto_label_threshold", err.Error())
	}
}

func TestTrainExportCmd_NoLabelsReturnsError(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.Close()

	if _, err := testsupport.Execute(t, "--config", configPath, "train", "build", "--out", filepath.Join(t.TempDir(), "dataset.yml")); err == nil {
		t.Error("Execute() error = nil, want an error when there are no human labels to export")
	}
}

func TestTrainExportCmd_IncludesApprovedSilverAndCapsPerCluster(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := articles.Save(ctx, article.Article{
		ID: "gold1", OriginalURL: "https://example.com/gold1", CanonicalURL: "https://example.com/gold1",
		Source:    source.Source{ID: testsupport.MustID(t, source.TypeRSS, "gold-source"), Type: source.TypeRSS},
		FetchedAt: now, PublishedAt: now, Status: article.StatusFetched, Domain: "gold.example.com",
	}); err != nil {
		t.Fatalf("seed gold article: %v", err)
	}
	if err := articles.Save(ctx, article.Article{
		ID: "silver1", OriginalURL: "https://example.com/silver1", CanonicalURL: "https://example.com/silver1",
		Source:    source.Source{ID: testsupport.MustID(t, source.TypeRSS, "silver-source"), Type: source.TypeRSS},
		FetchedAt: now.Add(time.Hour), PublishedAt: now.Add(time.Hour), Status: article.StatusFetched, Domain: "silver.example.com",
	}); err != nil {
		t.Fatalf("seed silver article: %v", err)
	}
	// current_cluster_id has a foreign key onto event_clusters(id), so
	// cluster membership must be established via ClusterRepository rather
	// than by setting article.Article.ClusterID directly on Save (see
	// article_repository_test.go's identical note).
	if err := clusters.CreateCluster(ctx, "c1", "gold1", clustering.StatusOpen, now); err != nil {
		t.Fatalf("create cluster c1: %v", err)
	}
	if err := clusters.AddMember(ctx, "c1", "silver1", clustering.MemberRoleSubstantialUpdate, 0.9, now.Add(time.Hour)); err != nil {
		t.Fatalf("add silver1 to c1: %v", err)
	}

	labels := sqliteadapter.NewHumanLabelRepository(db)
	if err := labels.Label(ctx, training.Example{ArticleID: "gold1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("save gold label: %v", err)
	}

	proposals := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)
	if _, err := proposals.Save(ctx, editoriallabelproposal.Proposal{
		ArticleID: "silver1", Label: "publish", Confidence: 0.97, Model: "m", PromptVersion: "v1", PolicyVersion: "p1",
		Status: editoriallabelproposal.StatusPending, CreatedAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save proposal: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "dataset.yml")
	out, err := testsupport.Execute(t, "--config", configPath, "train", "build", "--out", outPath, "--max-per-cluster", "1")
	if err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}

	dataset, err := editorialtrain.LoadDataset(outPath)
	if err != nil {
		t.Fatalf("LoadDataset(%s) error = %v", outPath, err)
	}
	if len(dataset.Examples) != 1 || dataset.Examples[0].ArticleID != "gold1" || dataset.Examples[0].Tier != "gold" {
		t.Errorf("dataset.Examples = %+v, want only gold1 (--max-per-cluster=1 keeps Gold over Silver in cluster c1)", dataset.Examples)
	}

	manifestData, err := os.ReadFile(outPath + ".manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest editorialtrain.DatasetManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.GoldCount != 1 || manifest.SilverCount != 0 {
		t.Errorf("manifest Gold/Silver counts = %d/%d, want 1/0 (silver1 capped out of the dataset)", manifest.GoldCount, manifest.SilverCount)
	}

	wantBreakdown := "built 1 examples to " + outPath + " (1 gold, 1 silver; 1 dropped by --max-per-cluster)"
	if !strings.Contains(out, wantBreakdown) {
		t.Errorf("output = %q, want it to contain the gold/silver/dropped breakdown %q", out, wantBreakdown)
	}
}

func writeTrainRunDataset(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("examples:\n")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		b.WriteString("  - article_id: p" + string(rune('a'+i)) + "\n    title: \"isotopes win big game today\"\n    label: publish\n    published_at: " + base.AddDate(0, 0, i).Format(time.RFC3339) + "\n")
	}
	for i := 0; i < 10; i++ {
		b.WriteString("  - article_id: r" + string(rune('a'+i)) + "\n    title: \"sponsored shop now huge discount\"\n    label: reject\n    published_at: " + base.AddDate(0, 0, i).Format(time.RFC3339) + "\n")
	}
	path := filepath.Join(t.TempDir(), "train_dataset.yml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	return path
}

func TestTrainRunCmd_WritesCandidateDirectoryPerAlgorithm(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	datasetPath := writeTrainRunDataset(t)
	outDir := t.TempDir()

	out, err := testsupport.Execute(t, "--config", configPath, "train", "evaluate",
		"--dataset", datasetPath, "--out", outDir, "--algorithm", "naive_bayes", "--seed", "42", "--accept-target-precision", "0.7", "--reject-target-precision", "0.7")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "naive_bayes") {
		t.Errorf("output = %q, want it to mention naive_bayes", out)
	}

	candidateDir := filepath.Join(outDir, "naive_bayes")
	m, err := editorialmodel.Load(candidateDir)
	if err != nil {
		t.Fatalf("editorialmodel.Load(%s) error = %v", candidateDir, err)
	}
	if m.Algorithm != "naive_bayes" {
		t.Errorf("loaded candidate Algorithm = %q, want %q", m.Algorithm, "naive_bayes")
	}
}

func TestTrainRunCmd_RegistersEachCandidateInModelRegistry(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	datasetPath := writeTrainRunDataset(t)
	outDir := t.TempDir()

	if _, err := testsupport.Execute(t, "--config", configPath, "train", "evaluate",
		"--dataset", datasetPath, "--out", outDir, "--algorithm", "naive_bayes", "--seed", "1", "--accept-target-precision", "0.7", "--reject-target-precision", "0.7"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	registry := sqliteadapter.NewModelRegistryRepository(db)
	candidates, err := registry.ListByStatus(context.Background(), "candidate")
	if err != nil {
		t.Fatalf("ListByStatus() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Algorithm != "naive_bayes" {
		t.Errorf("candidates = %+v, want exactly one naive_bayes candidate registered", candidates)
	}
}

func TestTrainRunCmd_MissingDatasetFileReturnsError(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	if _, err := testsupport.Execute(t, "--config", configPath, "train", "evaluate", "--dataset", filepath.Join(t.TempDir(), "missing.yml"), "--out", t.TempDir()); err == nil {
		t.Error("Execute() error = nil, want an error for a missing dataset file")
	}
}

// writeDistinctTrainRunDataset is like writeTrainRunDataset, but every
// example's title carries a distinct trailing index rather than the same
// fixed text repeated for every same-label example. Split's leakage
// grouping treats any two examples with byte-identical Title+Body as an
// exact-duplicate group and forces the whole group into a single
// partition (see dedup.go's exactDupKey) — unconditionally, independent of
// NearDupThreshold — so a dataset that reuses one title per label (as
// writeTrainRunDataset does) collapses into two groups and can strand an
// entire label out of the validate partition. The tests below need both
// labels present in validate for SelectThresholds to clear its target
// precision instead of falling back to the fully ambiguous {0, 100} band,
// so this variant keeps every example's text genuinely distinct.
func writeDistinctTrainRunDataset(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("examples:\n")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const n = 15
	for i := 0; i < n; i++ {
		b.WriteString("  - article_id: p" + string(rune('a'+i)) + "\n    title: \"isotopes win big game article number " + fmt.Sprint(i) + "\"\n    label: publish\n    published_at: " + base.AddDate(0, 0, i).Format(time.RFC3339) + "\n")
		b.WriteString("  - article_id: r" + string(rune('a'+i)) + "\n    title: \"sponsored shop now huge discount deal number " + fmt.Sprint(i) + "\"\n    label: reject\n    published_at: " + base.AddDate(0, 0, i).Format(time.RFC3339) + "\n")
	}
	path := filepath.Join(t.TempDir(), "distinct_train_dataset.yml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	return path
}

func TestTrainRunCmd_CostGridPrintsMinimizingPair(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	datasetPath := writeDistinctTrainRunDataset(t)
	outDir := t.TempDir()

	out, err := testsupport.Execute(t, "--config", configPath, "train", "evaluate",
		"--dataset", datasetPath, "--out", outDir, "--algorithm", "naive_bayes", "--seed", "42",
		"--accept-target-precision", "0.7", "--reject-target-precision", "0.7", "--cost-grid", "0.7,0.9")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "cost-grid winner:") {
		t.Fatalf("output = %q, want a cost-grid winner line", out)
	}

	// Independently recompute the expected minimizing pair by replaying
	// train run's own sweep (same Split config it hardcodes, same
	// editorial_costs from the fixture) against the naive_bayes candidate
	// it actually wrote, so this test verifies the printed numbers are
	// correct — not just that some line was printed.
	candidateDir := filepath.Join(outDir, "naive_bayes")
	m, err := editorialmodel.Load(candidateDir)
	if err != nil {
		t.Fatalf("editorialmodel.Load(%s) error = %v", candidateDir, err)
	}
	dataset, err := editorialtrain.LoadDataset(datasetPath)
	if err != nil {
		t.Fatalf("LoadDataset(%s) error = %v", datasetPath, err)
	}
	_, validate, _ := editorialtrain.Split(dataset, editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15})
	if !containsBothLabels(validate.Examples) {
		t.Fatalf("validate.Examples = %+v, want both publish and reject present (fixture bug, not a behavior bug)", validate.Examples)
	}
	costs := editorialtrain.CostConfig{FalsePublishCost: 3, MissedNewsCost: 1} // matches cmd_test.go's testsupport.FixtureTemplate editorial_costs
	values := []float64{0.7, 0.9}

	var best editorial.Thresholds
	bestCost := math.Inf(1)
	for _, accept := range values {
		for _, reject := range values {
			th := editorialtrain.SelectThresholds(m, validate.Examples, editorialtrain.ThresholdConfig{AcceptTargetPrecision: accept, RejectTargetPrecision: reject})
			candidate := m
			candidate.Thresholds = th
			report := editorialtrain.Evaluate(candidate, validate.Examples, nil)
			cost := report.ExpectedCost(costs)
			if cost < bestCost {
				bestCost = cost
				best = th
			}
		}
	}

	wantLine := fmt.Sprintf("%-20s cost-grid winner: accept=%.2f reject=%.2f expected_cost=%.2f", "naive_bayes", best.Accept, best.Reject, bestCost)
	if !strings.Contains(out, wantLine) {
		t.Errorf("output = %q, want it to contain %q", out, wantLine)
	}
}

// rewriteFixtureTinyArtifactSizeGate rewrites the fixture's
// editorial_gates max_artifact_size_bytes down to 1 byte, so tests can
// force every swept cost-grid pair to fail the promotion gates
// unconditionally (any real candidate directory is far larger than one
// byte) without hand-rolling a whole new config template or fighting
// config.Validate's cross-field precision/canary_min_precision ordering
// requirement.
func rewriteFixtureTinyArtifactSizeGate(t *testing.T, srcConfigPath, dstConfigPath string) {
	t.Helper()
	data, err := os.ReadFile(srcConfigPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rewritten := strings.Replace(string(data), "  max_artifact_size_bytes: 1048576\n", "  max_artifact_size_bytes: 1\n", 1)
	if rewritten == string(data) {
		t.Fatal("rewriteFixtureTinyArtifactSizeGate: max_artifact_size_bytes line not found in fixture — check cmd_test.go's testsupport.FixtureTemplate hasn't changed shape")
	}
	if err := os.WriteFile(dstConfigPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write rewritten fixture: %v", err)
	}
}

// rewriteFixtureZeroEditorialCosts rewrites the fixture's editorial_costs
// to their zero value, simulating a manually-crafted config that bypasses
// config.Validate's real "> 0" requirement — the scenario the --cost-grid
// unconfigured-costs guard defends against.
func rewriteFixtureZeroEditorialCosts(t *testing.T, srcConfigPath, dstConfigPath string) {
	t.Helper()
	data, err := os.ReadFile(srcConfigPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rewritten := strings.Replace(string(data), "editorial_costs:\n  false_publish_cost: 3\n  missed_news_cost: 1\n", "editorial_costs:\n  false_publish_cost: 0\n  missed_news_cost: 0\n", 1)
	if rewritten == string(data) {
		t.Fatal("rewriteFixtureZeroEditorialCosts: editorial_costs block not found in fixture — check cmd_test.go's testsupport.FixtureTemplate hasn't changed shape")
	}
	if err := os.WriteFile(dstConfigPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write rewritten fixture: %v", err)
	}
}

func TestTrainRunCmd_CostGridSkipsPairsFailingGatesAndReportsNoWinner(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	overriddenConfig := filepath.Join(dir, "settings-strict-precision.yml")
	// A 1-byte max_artifact_size_bytes gate forces every swept pair to
	// fail the gate check (any real candidate directory is far larger),
	// proving the sweep no longer picks a gate-failing "winner" by cost
	// alone.
	rewriteFixtureTinyArtifactSizeGate(t, configPath, overriddenConfig)
	datasetPath := writeDistinctTrainRunDataset(t)
	outDir := t.TempDir()

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "evaluate",
		"--dataset", datasetPath, "--out", outDir, "--algorithm", "naive_bayes", "--seed", "42",
		"--accept-target-precision", "0.7", "--reject-target-precision", "0.7", "--cost-grid", "0.7,0.9")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out, "cost-grid winner:") {
		t.Errorf("output = %q, want no cost-grid winner line when every swept pair fails the min_precision=1.0 gate", out)
	}
	if !strings.Contains(out, "cost-grid: no pair among the swept grid clears the promotion gates") {
		t.Errorf("output = %q, want the no-pair-clears-gates message", out)
	}
}

func TestTrainRunCmd_CostGridRequiresConfiguredCosts(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	overriddenConfig := filepath.Join(dir, "settings-zero-costs.yml")
	rewriteFixtureZeroEditorialCosts(t, configPath, overriddenConfig)
	datasetPath := writeDistinctTrainRunDataset(t)
	outDir := t.TempDir()

	_, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "evaluate",
		"--dataset", datasetPath, "--out", outDir, "--algorithm", "naive_bayes", "--seed", "42",
		"--accept-target-precision", "0.7", "--reject-target-precision", "0.7", "--cost-grid", "0.7,0.9")
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when --cost-grid is used with unconfigured (zero-value) editorial_costs")
	}
	if !strings.Contains(err.Error(), "editorial_costs") {
		t.Errorf("Execute() error = %q, want it to mention editorial_costs", err.Error())
	}
}

func containsBothLabels(examples []editorialtrain.TrainingExample) bool {
	var hasPublish, hasReject bool
	for _, e := range examples {
		if e.Label == "publish" {
			hasPublish = true
		}
		if e.Label == "reject" {
			hasReject = true
		}
	}
	return hasPublish && hasReject
}

func TestTrainRunCmd_PrintsCurrentPromotedRow(t *testing.T) {
	configPath := testsupport.WriteFixture(t) // model.path is "" in the base fixture; override below
	dir := filepath.Dir(configPath)
	promotedPath := filepath.Join(dir, "models", "editorial", "current")
	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, promotedPath)

	// Save a promoted model artifact directly at promotedPath — train run
	// reads the artifact and evaluates it on the same held-out examples as
	// the candidate, so there's no need to go through promote-model.
	promotedM := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}, Thresholds: editorial.Thresholds{Reject: 20, Accept: 80}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "promoted"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(promotedPath, promotedM, manifest, testsupport.PassingGateReportJSON(), schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	datasetPath := writeDistinctTrainRunDataset(t)
	outDir := t.TempDir()

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "evaluate",
		"--dataset", datasetPath, "--out", outDir, "--algorithm", "naive_bayes", "--seed", "42",
		"--accept-target-precision", "0.7", "--reject-target-precision", "0.7")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !strings.Contains(out, "(current promoted)") || !strings.Contains(out, "| n/a") {
		t.Errorf("output = %q, want a current-promoted baseline row", out)
	}
}

func TestTrainShadowCompareCmd_ReportsDisagreements(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// SeedArticleForLabeling doesn't set a Source, which leaves it at its
	// zero value; Save always serializes that as the literal source_id
	// string ":" (source.ID.String() joins platform and identifier with
	// ':' unconditionally), and reading it back via ListSince rejects
	// that with "invalid source_id" (source.ParseID requires a non-empty
	// platform before the ':'). Re-save the same article with a real,
	// parseable Source so shadow-compare's ListSince call can read it
	// back; every other field mirrors what SeedArticleForLabeling wrote.
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: articleID, OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		FetchedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status: article.StatusFetched,
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "example-source"), Name: "Example Source", Type: source.TypeRSS},
	}); err != nil {
		t.Fatalf("re-save article with source: %v", err)
	}

	// The seeded article has Title "" (SeedArticleForLabeling doesn't set
	// one), so both models below score it purely on bias -- give
	// production a strongly-positive bias (always accept) and candidate a
	// strongly-negative one (always reject), guaranteeing one recorded
	// disagreement.
	prodDir := filepath.Join(t.TempDir(), "production")
	prodModel := editorial.Model{Version: "nb-prod", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Bias: 100, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 30, Accept: 70}}
	if err := editorialmodel.Save(prodDir, prodModel, editorialmodel.Manifest{ID: "nb-prod", Version: "nb-prod", Algorithm: "naive_bayes", Status: "promoted"}, testsupport.PassingGateReportJSON(), editorialmodel.FeatureSchema{Version: "fs-1"}); err != nil {
		t.Fatalf("save production model: %v", err)
	}

	candDir := filepath.Join(t.TempDir(), "candidate")
	candModel := editorial.Model{Version: "nb-candidate", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Bias: -100, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 30, Accept: 70}}
	if err := editorialmodel.Save(candDir, candModel, editorialmodel.Manifest{ID: "nb-candidate", Version: "nb-candidate", Algorithm: "naive_bayes", Status: "candidate"}, testsupport.PassingGateReportJSON(), editorialmodel.FeatureSchema{Version: "fs-1"}); err != nil {
		t.Fatalf("save candidate model: %v", err)
	}

	rewrittenConfigPath := filepath.Join(filepath.Dir(configPath), "settings-with-model.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, rewrittenConfigPath, prodDir)

	out, err := testsupport.Execute(t, "--config", rewrittenConfigPath, "train", "shadow-compare", "--candidate", candDir, "--days", "365")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "1 article") || !strings.Contains(out, "1 disagreement") {
		t.Errorf("output = %q, want it to mention 1 article and 1 disagreement", out)
	}

	shadowRepo := sqliteadapter.NewShadowComparisonRepository(db)
	rows, err := shadowRepo.ListByRun(context.Background(), extractRunID(t, out))
	if err != nil {
		t.Fatalf("ListByRun() error = %v", err)
	}
	if len(rows) != 1 || rows[0].ArticleID != articleID {
		t.Fatalf("ListByRun() = %+v, want one row for %s", rows, articleID)
	}
	if rows[0].ProductionZone != "accept" || rows[0].CandidateZone != "reject" {
		t.Errorf("rows[0] zones = production %q candidate %q, want accept/reject", rows[0].ProductionZone, rows[0].CandidateZone)
	}
}

// extractRunID pulls the "run <id>" token the shadow-compare command
// prints out of its output, for the test to look the persisted rows up
// by the same run ID the command itself generated.
func extractRunID(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, "run ")
	if idx < 0 {
		t.Fatalf("output %q does not contain \"run \"", out)
	}
	rest := out[idx+len("run "):]
	// The printed message is "...run <id>: N article(s)...", so the run ID
	// is immediately followed by ':' with no space -- cut on ':' (or ' '/
	// '\n' as a fallback) rather than only whitespace, or the extracted
	// token would include the trailing colon and ListByRun would look up
	// the wrong ID.
	end := strings.IndexAny(rest, " :\n")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}
