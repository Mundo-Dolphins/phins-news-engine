package trainrun_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func TestGatherAutoSignals_NoPriorRun(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, _, _ := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	got, err := trainrun.GatherAutoSignals(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("GatherAutoSignals() error = %v", err)
	}
	// goldFixture() (used by baseDeps' fakeHumanLabelRepo) has 20
	// editorial examples (10 publish, 10 reject) and no prior run, so
	// every one of them counts as "new".
	if got.NewGoldSinceLastRun != 20 {
		t.Errorf("NewGoldSinceLastRun = %d, want 20 (no prior run: baseline is 0)", got.NewGoldSinceLastRun)
	}
	if got.NewSilverSinceLastRun != 0 {
		t.Errorf("NewSilverSinceLastRun = %d, want 0 (no proposals in baseDeps' fakeProposalRepo)", got.NewSilverSinceLastRun)
	}
	if got.HardExampleCount != 0 {
		t.Errorf("HardExampleCount = %d, want 0 (placeholder until the mining sub-project lands)", got.HardExampleCount)
	}
}

func TestGatherAutoSignals_DiffsAgainstLastRun(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, _, runRepo := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	runRepo.saved = append(runRepo.saved, training.Run{
		ID: "run-0", StartedAt: time.Now().Add(-24 * time.Hour),
		DatasetSummary: `{"gold":15,"silver":3,"hash":"deadbeef"}`, Status: "succeeded",
	})
	// fakeTrainingRunRepo.Latest ignores insertion order and always
	// returns nil unless overridden -- point it at the row above
	// directly via a small subtype so this test exercises the real
	// "diff against the last run" path instead of the "no prior run"
	// path already covered above.
	latestRepo := &fixedLatestRunRepo{run: &runRepo.saved[0]}
	deps.RunRepo = latestRepo

	got, err := trainrun.GatherAutoSignals(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("GatherAutoSignals() error = %v", err)
	}
	// goldFixture() has 20 Gold examples; baseline was 15 -> 5 new.
	if got.NewGoldSinceLastRun != 5 {
		t.Errorf("NewGoldSinceLastRun = %d, want 5 (20 current - 15 baseline)", got.NewGoldSinceLastRun)
	}
	// No silver proposals in this test's fakeProposalRepo (0 current);
	// baseline was 3 -> clamped to 0, never negative.
	if got.NewSilverSinceLastRun != 0 {
		t.Errorf("NewSilverSinceLastRun = %d, want 0 (clamped: 0 current - 3 baseline)", got.NewSilverSinceLastRun)
	}
}

func TestGatherAutoSignals_NovelVocabularyFromQueue(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, _, _ := baseDeps(proposals)
	deps.Queue = &fakeQueueRepo{entries: []reviewpriority.QueueEntry{
		{ArticleID: "a1", Reason: "novel_vocabulary=0.40; recency=0.10"},
		{ArticleID: "a2", Reason: "novel_vocabulary=0.35"},
		{ArticleID: "a3", Reason: "disagreement=0.50"},
	}}
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	got, err := trainrun.GatherAutoSignals(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("GatherAutoSignals() error = %v", err)
	}
	if got.NovelVocabularyCount != 2 {
		t.Errorf("NovelVocabularyCount = %d, want 2", got.NovelVocabularyCount)
	}
}

func TestGatherAutoSignals_ManualErrorRateFromPromotedModel(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, _, _ := baseDeps(proposals)
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})
	// No model at opts.ModelPath (t.TempDir()/model, nothing written) --
	// LoadPromotedReport returns nil, so ManualErrorRate must default
	// to 0 rather than erroring or panicking.

	got, err := trainrun.GatherAutoSignals(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("GatherAutoSignals() error = %v", err)
	}
	if got.ManualErrorRate != 0 {
		t.Errorf("ManualErrorRate = %v, want 0 when there is no promoted model yet", got.ManualErrorRate)
	}
}

// TestGatherAutoSignals_DisagreementRateUsesPostCapDenominator locks in
// the fix for the review finding that GatherAutoSignals' DisagreementRate
// used the raw pre-CapPerCluster gold+silver count as its denominator,
// while trainrun.Run's report computes the same-named metric against the
// post-CapPerCluster len(examples) -- a smaller number whenever
// opts.MaxPerCluster (1, via baseOptions) caps a cluster. This test gives
// 10 Gold examples the same non-empty ClusterID so CapPerCluster keeps
// only 1 of them, and asserts DisagreementRate is computed against that
// capped total (11: 1 survivor + the other 10 uncapped/no-cluster
// examples from goldFixture), not the raw 20.
func TestGatherAutoSignals_DisagreementRateUsesPostCapDenominator(t *testing.T) {
	labels := &fakeHumanLabelRepo{editorialExamples: clusteredGoldFixture()}
	proposals := &fakeProposalRepo{}
	deps, _, _, _ := baseDepsWithLabels(proposals, labels)
	deps.Queue = &fakeQueueRepo{entries: []reviewpriority.QueueEntry{
		{ArticleID: "q1", Reason: "disagreement=0.50"},
	}}
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	got, err := trainrun.GatherAutoSignals(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("GatherAutoSignals() error = %v", err)
	}
	// 10 "publish" examples share ClusterID "c1" and opts.MaxPerCluster
	// is 1 (baseOptions), so CapPerCluster keeps exactly 1 of them; the
	// 10 "reject" examples carry no ClusterID and are never capped.
	// Post-cap total = 1 + 10 = 11, so rate = 1/11, not 1/20.
	wantRate := 1.0 / 11.0
	if got.DisagreementRate != wantRate {
		t.Errorf("DisagreementRate = %v, want %v (1 disagreement / 11 post-cap examples, matching trainrun.Run's report denominator)", got.DisagreementRate, wantRate)
	}
}

// clusteredGoldFixture is goldFixture with its 10 "publish" examples
// forced into one shared, non-empty ClusterID so CapPerCluster (called
// with baseOptions' MaxPerCluster: 1) caps them down to one survivor --
// exercising the post-cap denominator path that an empty-ClusterID
// fixture like goldFixture cannot (CapPerCluster never caps
// empty-ClusterID examples).
func clusteredGoldFixture() []training.EditorialExample {
	examples := goldFixture()
	for i := range examples {
		if examples[i].Label == "publish" {
			examples[i].ClusterID = "c1"
		}
	}
	return examples
}

// fixedLatestRunRepo wraps a single fixed *training.Run for Latest(),
// delegating Save to a no-op -- used only by
// TestGatherAutoSignals_DiffsAgainstLastRun to avoid depending on
// fakeTrainingRunRepo's insertion-order behavior for this one test.
type fixedLatestRunRepo struct{ run *training.Run }

func (f *fixedLatestRunRepo) Save(context.Context, training.Run) error { return nil }
func (f *fixedLatestRunRepo) Latest(context.Context) (*training.Run, error) {
	return f.run, nil
}

func (f *fixedLatestRunRepo) LatestSucceeded(context.Context) (*training.Run, error) {
	return f.run, nil
}

func TestGatherAutoSignals_HardExampleCountFromRepo(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, _, _ := baseDeps(proposals)
	deps.HardExamples = &fixedUnresolvedCountRepo{count: 7}
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	got, err := trainrun.GatherAutoSignals(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("GatherAutoSignals() error = %v", err)
	}
	if got.HardExampleCount != 7 {
		t.Errorf("HardExampleCount = %d, want 7 (from deps.HardExamples.UnresolvedCount)", got.HardExampleCount)
	}
}

func TestGatherAutoSignals_NilHardExamplesRepoDefaultsToZero(t *testing.T) {
	proposals := &fakeProposalRepo{}
	deps, _, _ := baseDeps(proposals) // deps.HardExamples left nil, as every pre-existing test in this package does
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	got, err := trainrun.GatherAutoSignals(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("GatherAutoSignals() error = %v", err)
	}
	if got.HardExampleCount != 0 {
		t.Errorf("HardExampleCount = %d, want 0 when deps.HardExamples is nil", got.HardExampleCount)
	}
}

// fixedUnresolvedCountRepo satisfies out.HardExampleRepository with a
// fixed UnresolvedCount, for TestGatherAutoSignals_HardExampleCountFromRepo.
type fixedUnresolvedCountRepo struct{ count int }

func (f *fixedUnresolvedCountRepo) Upsert(context.Context, training.HardExampleCandidate) error {
	return nil
}

func (f *fixedUnresolvedCountRepo) UnresolvedCount(context.Context) (int, error) { return f.count, nil }

func (f *fixedUnresolvedCountRepo) ListUnresolved(context.Context, int) ([]training.HardExampleCandidate, error) {
	return nil, nil
}

func (f *fixedUnresolvedCountRepo) IsUnresolved(context.Context, string) (bool, error) {
	return false, nil
}

func (f *fixedUnresolvedCountRepo) MarkResolved(context.Context, string, time.Time) error { return nil }
