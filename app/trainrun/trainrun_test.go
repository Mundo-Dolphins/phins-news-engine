package trainrun_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

// ---- fakes: one per port in trainrun.Deps ----

type fakeHumanLabelRepo struct {
	editorialExamples   []training.EditorialExample
	unresolvedConflicts int
}

func (f *fakeHumanLabelRepo) Label(context.Context, training.Example) error        { return nil }
func (f *fakeHumanLabelRepo) Relabel(context.Context, training.Example, int) error { return nil }
func (f *fakeHumanLabelRepo) Retract(context.Context, string, int) error           { return nil }
func (f *fakeHumanLabelRepo) GetActive(context.Context, string) (training.Example, bool, error) {
	return training.Example{}, false, nil
}

func (f *fakeHumanLabelRepo) ListByArticle(context.Context, string) ([]training.Example, error) {
	return nil, nil
}

func (f *fakeHumanLabelRepo) ListAll(context.Context) ([]training.Example, error) { return nil, nil }

func (f *fakeHumanLabelRepo) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return f.editorialExamples, nil
}

func (f *fakeHumanLabelRepo) CountEditorialExamples(context.Context) (int, error) {
	return len(f.editorialExamples), nil
}

func (f *fakeHumanLabelRepo) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}

func (f *fakeHumanLabelRepo) CountUnresolvedConflicts(context.Context) (int, error) {
	return f.unresolvedConflicts, nil
}

func (f *fakeHumanLabelRepo) ResolveConflict(context.Context, string, training.Example) error {
	return nil
}

var _ out.HumanLabelRepository = (*fakeHumanLabelRepo)(nil)

// fakeArticleRepo's candidates back ListCandidatesForReview, the pool
// trainmine.Mine scans; byID backs FindByID, which trainrun's titlesFor
// uses to resolve a hard positive's title for the report's
// unknown-terms nudge.
type fakeArticleRepo struct {
	byID       map[string]article.Article
	candidates []article.Article
}

func (f *fakeArticleRepo) Save(context.Context, article.Article) error { return nil }
func (f *fakeArticleRepo) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f.byID[id]
	if !ok {
		return article.Article{}, sql.ErrNoRows
	}
	return a, nil
}

func (f *fakeArticleRepo) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeArticleRepo) Count(context.Context) (int, error) { return len(f.byID), nil }
func (f *fakeArticleRepo) ListCandidatesForReview(_ context.Context, _ time.Time, limit int) ([]article.Article, error) {
	if limit > 0 && limit < len(f.candidates) {
		return f.candidates[:limit], nil
	}
	return f.candidates, nil
}

func (f *fakeArticleRepo) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticleRepo) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, sql.ErrNoRows
}

var _ out.ArticleRepository = (*fakeArticleRepo)(nil)

type fakeModelRegistryRepo struct{ registered []training.ModelMetadata }

func (f *fakeModelRegistryRepo) Register(_ context.Context, m training.ModelMetadata) error {
	f.registered = append(f.registered, m)
	return nil
}

func (f *fakeModelRegistryRepo) Get(_ context.Context, version string) (training.ModelMetadata, error) {
	for _, m := range f.registered {
		if m.Version == version {
			return m, nil
		}
	}
	return training.ModelMetadata{}, sql.ErrNoRows
}

func (f *fakeModelRegistryRepo) ListByStatus(_ context.Context, status string) ([]training.ModelMetadata, error) {
	var got []training.ModelMetadata
	for _, m := range f.registered {
		if m.Status == status {
			got = append(got, m)
		}
	}
	return got, nil
}

var _ out.ModelRegistryRepository = (*fakeModelRegistryRepo)(nil)

type fakeTrainingRunRepo struct{ saved []training.Run }

func (f *fakeTrainingRunRepo) Save(_ context.Context, r training.Run) error {
	f.saved = append(f.saved, r)
	return nil
}
func (f *fakeTrainingRunRepo) Latest(context.Context) (*training.Run, error) { return nil, nil }
func (f *fakeTrainingRunRepo) LatestSucceeded(context.Context) (*training.Run, error) {
	return nil, nil
}

var _ out.TrainingRunRepository = (*fakeTrainingRunRepo)(nil)

type fakeUnitOfWork struct{}

func (fakeUnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

var _ out.UnitOfWork = fakeUnitOfWork{}

// fakeProposalRepo's listByStatusErr, when set, simulates the analyze
// stage's LLM-teacher-backed proposal source being unreachable (Part
// 34's "Ollama unavailable" scenario) -- trainrun.Run must abort before
// evaluate/promote and leave the registry/model file untouched.
type fakeProposalRepo struct {
	pending          []editoriallabelproposal.Proposal
	latestPerArticle []editoriallabelproposal.Proposal
	listByStatusErr  error
}

func (f *fakeProposalRepo) Save(context.Context, editoriallabelproposal.Proposal) (string, error) {
	return "", nil
}

func (f *fakeProposalRepo) GetLatestByArticle(context.Context, string) (editoriallabelproposal.Proposal, bool, error) {
	return editoriallabelproposal.Proposal{}, false, nil
}

func (f *fakeProposalRepo) ListLatestPerArticle(context.Context) ([]editoriallabelproposal.Proposal, error) {
	return f.latestPerArticle, nil
}

func (f *fakeProposalRepo) ListByStatus(_ context.Context, status editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error) {
	if f.listByStatusErr != nil {
		return nil, f.listByStatusErr
	}
	if status != editoriallabelproposal.StatusPending {
		return nil, nil
	}
	return f.pending, nil
}

func (f *fakeProposalRepo) UpdateStatus(context.Context, string, editoriallabelproposal.Status, time.Time) error {
	return nil
}

func (f *fakeProposalRepo) FilterUnprocessed(_ context.Context, _ string, articleIDs []string) ([]string, error) {
	return articleIDs, nil
}

func (f *fakeProposalRepo) MarkArticlesProcessed(context.Context, string, []string, time.Time) error {
	return nil
}

var _ editoriallabelproposalport.EditorialLabelProposalRepository = (*fakeProposalRepo)(nil)

type fakeQueueRepo struct{ entries []reviewpriority.QueueEntry }

func (f *fakeQueueRepo) Upsert(context.Context, reviewpriority.QueueEntry) error { return nil }
func (f *fakeQueueRepo) Delete(context.Context, string) error                    { return nil }
func (f *fakeQueueRepo) ListOrderedByScore(_ context.Context, limit int) ([]reviewpriority.QueueEntry, error) {
	if limit > 0 && limit < len(f.entries) {
		return f.entries[:limit], nil
	}
	return f.entries, nil
}

var _ reviewqueueport.ReviewQueueRepository = (*fakeQueueRepo)(nil)

// ---- fixture + shared test setup ----

// goldFixture returns 10 publish + 10 reject examples using the same
// distinguishing vocabulary as modeltraining_test.go's sampleDataset --
// proven to train and clear a permissive gate set for every algorithm at
// the ratios/thresholds baseOptions configures below.
func goldFixture() []training.EditorialExample {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var examples []training.EditorialExample
	for i := 0; i < 10; i++ {
		examples = append(examples, training.EditorialExample{
			ArticleID: fmt.Sprintf("p%d", i), Title: "isotopes win big game today", Label: "publish",
			PublishedAt: base.AddDate(0, 0, i), FetchedAt: base.AddDate(0, 0, i), LabeledAt: base.AddDate(0, 0, i),
		})
	}
	for i := 0; i < 10; i++ {
		examples = append(examples, training.EditorialExample{
			ArticleID: fmt.Sprintf("r%d", i), Title: "sponsored shop now huge discount", Label: "reject",
			PublishedAt: base.AddDate(0, 0, i), FetchedAt: base.AddDate(0, 0, i), LabeledAt: base.AddDate(0, 0, i),
		})
	}
	return examples
}

func baseDeps(proposals *fakeProposalRepo) (trainrun.Deps, *fakeModelRegistryRepo, *fakeTrainingRunRepo) {
	deps, registry, runRepo, _ := baseDepsWithLabels(proposals, &fakeHumanLabelRepo{editorialExamples: goldFixture()})
	return deps, registry, runRepo
}

func baseDepsWithLabels(proposals *fakeProposalRepo, labels *fakeHumanLabelRepo) (trainrun.Deps, *fakeModelRegistryRepo, *fakeTrainingRunRepo, *fakeHumanLabelRepo) {
	registry := &fakeModelRegistryRepo{}
	runRepo := &fakeTrainingRunRepo{}
	deps := trainrun.Deps{
		Registry:  registry,
		RunRepo:   runRepo,
		UOW:       fakeUnitOfWork{},
		Labels:    labels,
		Articles:  &fakeArticleRepo{byID: map[string]article.Article{}},
		Proposals: proposals,
		Queue:     &fakeQueueRepo{},
	}
	return deps, registry, runRepo, labels
}

func baseOptions(t *testing.T, gateCfg editorialtrain.GateConfig, promotionCfg modelpromotion.PromotionCriteria) trainrun.Options {
	t.Helper()
	dir := t.TempDir()
	return trainrun.Options{
		DatasetPath: dir + "/dataset.yml", MaxPerCluster: 1, CandidatesDir: dir + "/candidates", ModelPath: dir + "/model",
		TrustedSourceIDs: map[string]bool{}, AutoLabelThreshold: 0.95,
		TrainCfg: editorialtrain.TrainConfig{
			Split:         editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15},
			Prune:         editorialtrain.PruneConfig{MinDocFreq: 0.01, MaxDocFreq: 0.9, MaxFeatures: 2000},
			Algorithms:    []string{"naive_bayes"},
			Thresholds:    editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.7, RejectTargetPrecision: 0.7},
			Seed:          42,
			SchemaVersion: "fs-test",
		},
		GateCfg: gateCfg, PromotionCfg: promotionCfg,
	}
}

// permissiveGateCfg mirrors modeltraining_test.go's own permissive gate
// config, deliberately wide enough that goldFixture's candidate passes.
func permissiveGateCfg() editorialtrain.GateConfig {
	return editorialtrain.GateConfig{
		MinPrecision: 0.0, MaxRecallRegression: 1.0, MinPRAUC: 0.0, MaxFalsePositiveRate: 1.0,
		MinPerSourcePrecision: 0.0, MinSourceExamples: 1000, MaxArtifactSizeBytes: 10_000_000, MaxInferenceNanos: 1_000_000_000,
	}
}

func TestTrainRun_AbortsCleanlyWhenLLMUnavailable(t *testing.T) {
	proposals := &fakeProposalRepo{listByStatusErr: fmt.Errorf("llmclient: connect: dial tcp 127.0.0.1:11434: connect: connection refused")}
	deps, registry, runRepo := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	_, err := trainrun.Run(context.Background(), deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))

	if err == nil {
		t.Fatal("Run() error = nil, want an error naming the analyze stage")
	}
	if !strings.HasPrefix(err.Error(), "trainrun: analyze:") {
		t.Errorf("Run() error = %q, want it to start with %q", err.Error(), "trainrun: analyze:")
	}
	if len(registry.registered) != 0 {
		t.Errorf("registry.registered = %+v, want zero calls (production untouched)", registry.registered)
	}
	if _, statErr := os.Stat(opts.ModelPath); statErr == nil {
		t.Errorf("production model directory %s exists, want it untouched (never created)", opts.ModelPath)
	}
	if len(runRepo.saved) != 1 || runRepo.saved[0].Status != "failed" {
		t.Errorf("runRepo.saved = %+v, want exactly one row with Status=\"failed\"", runRepo.saved)
	}
}

func TestTrainRun_PromotesWhenGatesPass(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, registry, runRepo := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{MinPrecision: 0.0})

	outcome, err := trainrun.Run(context.Background(), deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Run.Status != "succeeded" {
		t.Errorf("Run.Status = %q, want \"succeeded\"", outcome.Run.Status)
	}
	promoted := 0
	for _, m := range registry.registered {
		if m.Status == "promoted" {
			promoted++
		}
	}
	if promoted != 1 {
		t.Errorf("registry.registered = %+v, want exactly one \"promoted\" row", registry.registered)
	}
	if !strings.Contains(outcome.Report, "Recommendation: PROMOTE") {
		t.Errorf("Report = %q, want it to contain %q", outcome.Report, "Recommendation: PROMOTE")
	}
	if !strings.Contains(outcome.Report, "Dataset: 20 examples (20 Gold, 0 Silver)") {
		t.Errorf("Report = %q, want it to report the real 20-example Gold dataset", outcome.Report)
	}
	if !strings.HasPrefix(outcome.Run.ModelVersion, "naive_bayes-candidate-") {
		t.Errorf("Run.ModelVersion = %q, want the real promoted candidate's version", outcome.Run.ModelVersion)
	}
	if !strings.Contains(outcome.Report, "Candidate "+outcome.Run.ModelVersion+" vs. production (none)") {
		t.Errorf("Report = %q, want it to name candidate %q against no production baseline", outcome.Report, outcome.Run.ModelVersion)
	}
	if !strings.Contains(outcome.Report, "Result: promoted "+outcome.Run.ModelVersion) {
		t.Errorf("Report = %q, want a Result line naming the promoted version", outcome.Report)
	}
	if !strings.Contains(outcome.Run.DatasetSummary, `"gold":20,"silver":0`) {
		t.Errorf("Run.DatasetSummary = %q, want the real gold/silver counts", outcome.Run.DatasetSummary)
	}
	if outcome.Run.Report != outcome.Report {
		t.Errorf("Run.Report and Outcome.Report differ, want the same rendered text")
	}
	if len(runRepo.saved) != 1 || runRepo.saved[0].Status != "succeeded" {
		t.Errorf("runRepo.saved = %+v, want exactly one row with Status=\"succeeded\"", runRepo.saved)
	}
}

func TestTrainRun_RejectsWhenGatesFail(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, registry, runRepo := baseDeps(proposals)
	impossibleGates := editorialtrain.GateConfig{
		MinPrecision: 1.0, MaxRecallRegression: 0.0, MinPRAUC: 1.0, MaxFalsePositiveRate: 0.0,
		MinPerSourcePrecision: 1.0, MinSourceExamples: 1000, MaxArtifactSizeBytes: 10_000_000, MaxInferenceNanos: 1_000_000_000,
	}
	opts := baseOptions(t, impossibleGates, modelpromotion.PromotionCriteria{})

	outcome, err := trainrun.Run(context.Background(), deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run() error = %v (a gate rejection must be a successful run, not an error)", err)
	}
	if outcome.Run.Status != "succeeded" {
		t.Errorf("Run.Status = %q, want \"succeeded\"", outcome.Run.Status)
	}
	for _, m := range registry.registered {
		if m.Status == "promoted" {
			t.Errorf("registry.registered contains a \"promoted\" row %+v, want none", m)
		}
	}
	if _, statErr := os.Stat(opts.ModelPath); statErr == nil {
		t.Errorf("production model directory %s exists, want it untouched (never created)", opts.ModelPath)
	}
	if !strings.Contains(outcome.Report, "Recommendation: REJECT") {
		t.Errorf("Report = %q, want it to contain %q", outcome.Report, "Recommendation: REJECT")
	}
	if len(runRepo.saved) != 1 || runRepo.saved[0].Status != "succeeded" {
		t.Errorf("runRepo.saved = %+v, want exactly one row with Status=\"succeeded\"", runRepo.saved)
	}
}

// TestTrainRun_ManifestRecordsRealProvenanceAndWarns covers finding 6:
// `train run` hardcoded WriteManifest's unresolvedConflicts and
// silverSkippedMissingArticle to 0, silently overwriting `train build`'s
// honest manifest at the shared default path and dropping the
// unresolved-conflict operator warning.
func TestTrainRun_ManifestRecordsRealProvenanceAndWarns(t *testing.T) {
	proposals := &fakeProposalRepo{latestPerArticle: []editoriallabelproposal.Proposal{
		// Silver-tier (confidence >= AutoLabelThreshold) but its article
		// row is gone, so FetchSilverExamples skips and counts it.
		{ID: "prop-1", ArticleID: "missing-1", Label: "publish", Confidence: 0.99},
	}}
	labels := &fakeHumanLabelRepo{editorialExamples: goldFixture(), unresolvedConflicts: 3}
	deps, _, _, _ := baseDepsWithLabels(proposals, labels)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{MinPrecision: 0.0})

	outcome, err := trainrun.Run(context.Background(), deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	raw, err := os.ReadFile(opts.DatasetPath + ".manifest.json")
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	var manifest struct {
		UnresolvedConflictCount          int `json:"unresolved_conflict_count"`
		SilverSkippedMissingArticleCount int `json:"silver_skipped_missing_article_count"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("Unmarshal(manifest) error = %v", err)
	}
	if manifest.UnresolvedConflictCount != 3 {
		t.Errorf("manifest.unresolved_conflict_count = %d, want 3 (the real repository count, not a hardcoded 0)", manifest.UnresolvedConflictCount)
	}
	if manifest.SilverSkippedMissingArticleCount != 1 {
		t.Errorf("manifest.silver_skipped_missing_article_count = %d, want 1 (FetchSilverExamples' real count)", manifest.SilverSkippedMissingArticleCount)
	}

	joined := strings.Join(outcome.Warnings, "\n")
	if !strings.Contains(joined, "WARNING: 3 article(s) have an unresolved legacy label conflict") {
		t.Errorf("Outcome.Warnings = %q, want the same unresolved-conflict warning `train build` prints", outcome.Warnings)
	}
}

func TestTrainRun_InvokesMiningWhenWired(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, registry, runRepo := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	hardExamples := &fakeHardExampleRepoForTrainRun{unresolvedCount: 4}
	deps.HardExamples = hardExamples
	deps.LLM = &noOpTeacherLLM{} // returns zero labels for every batch -- mining runs, finds nothing, does not error

	outcome, err := trainrun.Run(context.Background(), deps, opts, time.Now().UTC())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !hardExamples.unresolvedCountCalled {
		t.Error("Run() did not call HardExamples.UnresolvedCount -- mining was not invoked even though Deps.LLM and Deps.HardExamples were both set")
	}
	// Finding 4: the count Run reads must actually reach the report,
	// rather than being discarded behind a comment claiming it does not.
	want := "Hard examples awaiting a human label: 4"
	if !strings.Contains(outcome.Report, want) {
		t.Errorf("Report missing %q\nfull report:\n%s", want, outcome.Report)
	}
	_ = registry
	_ = runRepo
}

// writeFixtureModel writes a minimal, valid promoted model artifact to
// dir, mirroring trainmine_test.go's helper of the same name:
// editorial.ExtractFeatures turns every lowercased word token in an
// article's Title+Summary into a "uni:<word>" feature, so a weights key
// like "uni:discardword" pairs with a test article whose Title contains
// that literal word.
func writeFixtureModel(t *testing.T, dir string, weights map[string]float64, thresholds editorial.Thresholds) {
	t.Helper()
	model := editorial.Model{
		Version: "nb-v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1",
		Bias: 0, Weights: weights, Thresholds: thresholds,
	}
	manifest := editorialmodel.Manifest{
		ID: "nb-v1", Version: "nb-v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1",
		TrainedAt: time.Now().UTC(), Thresholds: thresholds, Status: "promoted",
	}
	features := make([]string, 0, len(weights))
	for f := range weights {
		features = append(features, f)
	}
	if err := editorialmodel.Save(dir, model, manifest, `{}`, editorialmodel.FeatureSchema{Version: "fs-1", Features: features}); err != nil {
		t.Fatalf("editorialmodel.Save() error = %v", err)
	}
}

// scriptedTeacherLLM returns a fixed set of labels (or a fixed error)
// for every Classify call.
type scriptedTeacherLLM struct {
	labels []out.EditorialTeacherLabel
	err    error
}

func (s *scriptedTeacherLLM) Classify(_ context.Context, _ string, articles []out.EditorialTeacherArticle) (out.EditorialTeacherResult, error) {
	if s.err != nil {
		return out.EditorialTeacherResult{}, s.err
	}
	_ = articles
	return out.EditorialTeacherResult{Labels: s.labels}, nil
}

// TestTrainRun_MiningHardPositiveReachesReport covers the whole-branch
// review's Finding 6: TestTrainRun_InvokesMiningWhenWired left
// MiningBatchLimit at zero, so trainmine.Mine short-circuited before the
// LLM and Run's titlesFor -> trainmine.UnknownTerms -> UnknownTermsNote
// wiring was never exercised end-to-end.
func TestTrainRun_MiningHardPositiveReachesReport(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, _, _ := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{MinPrecision: 0.0})

	// A promoted model must already exist at opts.ModelPath for mining to
	// run at all -- Mine returns Skipped otherwise.
	writeFixtureModel(t, opts.ModelPath, map[string]float64{"uni:discardword": -8}, editorial.Thresholds{Reject: 20, Accept: 80})

	// NB reads this title as a confident reject; the LLM confidently says
	// publish -- a hard positive. "quarrington" is the unknown term the
	// report should surface; "discardword" is in KnownTerms and must not
	// appear.
	hardPositive := article.Article{ID: "hard-pos", Title: "quarrington discardword discardword discardword"}
	deps.Articles = &fakeArticleRepo{
		byID:       map[string]article.Article{"hard-pos": hardPositive},
		candidates: []article.Article{hardPositive},
	}
	deps.HardExamples = &fakeHardExampleRepoForTrainRun{}
	deps.LLM = &scriptedTeacherLLM{labels: []out.EditorialTeacherLabel{
		{ArticleID: "hard-pos", Label: "publish", Confidence: 0.99},
	}}
	opts.MiningBatchLimit = 10
	opts.MiningMinNBConfidence = 0.7
	opts.KnownTerms = map[string]bool{"discardword": true}

	outcome, err := trainrun.Run(context.Background(), deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := "Unknown terms in hard-positive articles: quarrington"
	if !strings.Contains(outcome.Report, want) {
		t.Errorf("Report missing %q\nfull report:\n%s", want, outcome.Report)
	}
	if strings.Contains(outcome.Report, "discardword") {
		t.Errorf("Report = %q, must not surface a term already in KnownTerms", outcome.Report)
	}
}

// TestTrainRun_MiningFailureAbortsRun covers the other half of Finding
// 6: the mine stage's fail(...) path was untested, even though an LLM
// error there aborts the whole promotion run.
func TestTrainRun_MiningFailureAbortsRun(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, registry, runRepo := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{MinPrecision: 0.0})
	writeFixtureModel(t, opts.ModelPath, map[string]float64{"uni:discardword": -8}, editorial.Thresholds{Reject: 20, Accept: 80})

	deps.Articles = &fakeArticleRepo{
		byID:       map[string]article.Article{},
		candidates: []article.Article{{ID: "a1", Title: "discardword discardword discardword"}},
	}
	deps.HardExamples = &fakeHardExampleRepoForTrainRun{}
	deps.LLM = &scriptedTeacherLLM{err: fmt.Errorf("llmclient: connect: dial tcp 127.0.0.1:11434: connect: connection refused")}
	opts.MiningBatchLimit = 10

	_, err := trainrun.Run(context.Background(), deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Run() error = nil, want an error naming the mine stage")
	}
	if !strings.HasPrefix(err.Error(), "trainrun: mine:") {
		t.Errorf("Run() error = %q, want it to start with %q", err.Error(), "trainrun: mine:")
	}
	if len(registry.registered) != 0 {
		t.Errorf("registry.registered = %+v, want zero calls (mining aborts before evaluate)", registry.registered)
	}
	if len(runRepo.saved) != 1 || runRepo.saved[0].Status != "failed" {
		t.Errorf("runRepo.saved = %+v, want exactly one row with Status=\"failed\"", runRepo.saved)
	}
}

// noOpTeacherLLM satisfies out.EditorialTeacherLLM by returning zero
// labels, used only to confirm Run reaches the mining call without
// requiring a real LLM fixture for this particular test.
type noOpTeacherLLM struct{}

func (noOpTeacherLLM) Classify(context.Context, string, []out.EditorialTeacherArticle) (out.EditorialTeacherResult, error) {
	return out.EditorialTeacherResult{}, nil
}

// fakeHardExampleRepoForTrainRun tracks whether UnresolvedCount was
// called, to prove Run's mining path actually ran (mining's own
// scan-and-upsert behavior is already covered by trainmine_test.go;
// this test only needs to prove trainrun.Run reaches it).
type fakeHardExampleRepoForTrainRun struct {
	unresolvedCountCalled bool
	unresolvedCount       int
}

func (f *fakeHardExampleRepoForTrainRun) Upsert(context.Context, training.HardExampleCandidate) error {
	return nil
}

func (f *fakeHardExampleRepoForTrainRun) UnresolvedCount(context.Context) (int, error) {
	f.unresolvedCountCalled = true
	return f.unresolvedCount, nil
}

func (f *fakeHardExampleRepoForTrainRun) ListUnresolved(context.Context, int) ([]training.HardExampleCandidate, error) {
	return nil, nil
}

func (f *fakeHardExampleRepoForTrainRun) IsUnresolved(context.Context, string) (bool, error) {
	return false, nil
}

func (f *fakeHardExampleRepoForTrainRun) MarkResolved(context.Context, string, time.Time) error {
	return nil
}
