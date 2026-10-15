package editorialtrain_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func trainDataset() editorialtrain.Dataset {
	var examples []editorialtrain.TrainingExample
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 15; i++ {
		examples = append(examples, editorialtrain.TrainingExample{
			ArticleID: "p" + string(rune('a'+i)), Title: "isotopes win game " + string(rune('a'+i)), Body: "details about game " + string(rune('a'+i)), Label: "publish",
			PublishedAt: base.AddDate(0, 0, i),
		})
	}
	for i := 0; i < 15; i++ {
		examples = append(examples, editorialtrain.TrainingExample{
			ArticleID: "r" + string(rune('a'+i)), Title: "discount shop " + string(rune('a'+i)), Body: "sales offer " + string(rune('a'+i)), Label: "reject",
			PublishedAt: base.AddDate(0, 0, i),
		})
	}
	return editorialtrain.Dataset{Examples: examples}
}

func defaultTrainConfig() editorialtrain.TrainConfig {
	return editorialtrain.TrainConfig{
		Split:         editorialtrain.SplitConfig{TrainRatio: 0.6, ValidateRatio: 0.2, TestRatio: 0.2},
		Prune:         editorialtrain.PruneConfig{MinDocFreq: 0, MaxDocFreq: 1, MaxFeatures: 0},
		Thresholds:    editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.8, RejectTargetPrecision: 0.8},
		Seed:          42,
		SchemaVersion: "fs-train-test",
	}
}

func TestTrain_AllFiveAlgorithmsByDefault(t *testing.T) {
	candidates, err := editorialtrain.Train(trainDataset(), defaultTrainConfig())
	if err != nil {
		t.Fatalf("Train() error = %v", err)
	}
	if len(candidates) != 5 {
		t.Fatalf("len(candidates) = %d, want 5", len(candidates))
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[c.Model.Algorithm] = true
		if c.Model.FeatureSchemaVersion != "fs-train-test" {
			t.Errorf("candidate %s: FeatureSchemaVersion = %q, want %q", c.Model.Algorithm, c.Model.FeatureSchemaVersion, "fs-train-test")
		}
		if c.DatasetHash == "" {
			t.Errorf("candidate %s: DatasetHash is empty", c.Model.Algorithm)
		}
		if c.Model.Thresholds.Accept <= c.Model.Thresholds.Reject {
			t.Errorf("candidate %s: Thresholds = %+v, want Accept > Reject", c.Model.Algorithm, c.Model.Thresholds)
		}
	}
	for _, want := range []string{"naive_bayes", "logistic_regression", "linear_svm", "nbsvm", "trivial_majority"} {
		if !seen[want] {
			t.Errorf("missing candidate for algorithm %q", want)
		}
	}
}

func TestTrain_RequestedAlgorithmsSubset(t *testing.T) {
	cfg := defaultTrainConfig()
	cfg.Algorithms = []string{"naive_bayes"}
	candidates, err := editorialtrain.Train(trainDataset(), cfg)
	if err != nil {
		t.Fatalf("Train() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Model.Algorithm != "naive_bayes" {
		t.Fatalf("candidates = %+v, want exactly one naive_bayes candidate", candidates)
	}
}

func TestTrain_UnknownAlgorithmReturnsError(t *testing.T) {
	cfg := defaultTrainConfig()
	cfg.Algorithms = []string{"not_a_real_algorithm"}
	if _, err := editorialtrain.Train(trainDataset(), cfg); err == nil {
		t.Error("Train() error = nil, want an error for an unrecognized algorithm name")
	}
}

func TestTrain_ReportsAreComputedOnValidateAndTestSeparately(t *testing.T) {
	cfg := defaultTrainConfig()
	cfg.Algorithms = []string{"naive_bayes"}
	candidates, err := editorialtrain.Train(trainDataset(), cfg)
	if err != nil {
		t.Fatalf("Train() error = %v", err)
	}
	c := candidates[0]
	validateTotal := c.ValidateReport.Overall.TP + c.ValidateReport.Overall.FP + c.ValidateReport.Overall.FN + c.ValidateReport.Overall.TN
	testTotal := c.TestReport.Overall.TP + c.TestReport.Overall.FP + c.TestReport.Overall.FN + c.TestReport.Overall.TN
	if validateTotal == 0 {
		t.Error("ValidateReport.Overall has zero examples counted")
	}
	if testTotal == 0 {
		t.Error("TestReport.Overall has zero examples counted")
	}
}

func TestTrain_RejectPolicyWithFPRCapSelectsAcceptByFPR(t *testing.T) {
	cfg := defaultTrainConfig()
	cfg.Algorithms = []string{"naive_bayes"}
	cfg.AmbiguousPolicy = "reject"
	cfg.Thresholds.MaxFalsePositiveRate = 0.15
	// Precision targets no split can reach: the two-sided sweep would fall
	// back to {0,100}; the FPR rule must not depend on them.
	cfg.Thresholds.AcceptTargetPrecision, cfg.Thresholds.RejectTargetPrecision = 1.01, 1.01

	candidates, err := editorialtrain.Train(trainDataset(), cfg)
	if err != nil {
		t.Fatalf("Train() error = %v", err)
	}
	got := candidates[0].Model.Thresholds
	if got.Accept == 100 {
		t.Fatalf("Thresholds = %+v, want an FPR-selected accept boundary, not the fully-ambiguous fallback", got)
	}
	if fpr := candidates[0].ValidateReport.Overall; fpr.FP > 0 && float64(fpr.FP)/float64(fpr.FP+fpr.TN) > 0.15 {
		t.Errorf("validate FPR = %d/%d, want <= 0.15", fpr.FP, fpr.FP+fpr.TN)
	}
}

func TestSplitForTraining_DropsExamplesOutsideInferencePopulation(t *testing.T) {
	cfg := defaultTrainConfig()
	cfg.Relevant = func(ex editorialtrain.TrainingExample) bool { return ex.Title[:8] != "discount" }

	train, validate, test := editorialtrain.SplitForTraining(trainDataset(), cfg)
	total := len(train.Examples) + len(validate.Examples) + len(test.Examples)
	if total != 15 {
		t.Fatalf("kept %d examples, want 15 (the 15 off-population rejects dropped)", total)
	}
	for _, part := range []editorialtrain.Dataset{train, validate, test} {
		for _, ex := range part.Examples {
			if ex.Label == "reject" {
				t.Errorf("example %s survived the Relevant filter", ex.ArticleID)
			}
		}
	}
}

func TestSplitForTraining_NilRelevantKeepsEverything(t *testing.T) {
	train, validate, test := editorialtrain.SplitForTraining(trainDataset(), defaultTrainConfig())
	if total := len(train.Examples) + len(validate.Examples) + len(test.Examples); total != 30 {
		t.Errorf("kept %d examples, want all 30", total)
	}
}

func TestTrain_AppliesRelevantFilterBeforeFitting(t *testing.T) {
	cfg := defaultTrainConfig()
	cfg.Algorithms = []string{"naive_bayes"}
	cfg.Relevant = func(ex editorialtrain.TrainingExample) bool { return ex.Label == "publish" }
	candidates, err := editorialtrain.Train(trainDataset(), cfg)
	if err != nil {
		t.Fatalf("Train() error = %v", err)
	}
	for _, r := range []editorialtrain.Report{candidates[0].ValidateReport, candidates[0].TestReport} {
		if r.Overall.FP+r.Overall.TN != 0 {
			t.Errorf("report has %d reject examples, want 0 once rejects are filtered out", r.Overall.FP+r.Overall.TN)
		}
	}
}

func TestSplitForTraining_SilverOnlyInTrain_GoldPartitionsUnchanged(t *testing.T) {
	gold := trainDataset()
	wantTrain, wantValidate, wantTest := editorialtrain.SplitForTraining(gold, defaultTrainConfig())

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	withSilver := editorialtrain.Dataset{Examples: append([]editorialtrain.TrainingExample{}, gold.Examples...)}
	for i := 0; i < 10; i++ {
		withSilver.Examples = append(withSilver.Examples, editorialtrain.TrainingExample{
			ArticleID: "s" + string(rune('a'+i)), Title: "silver " + string(rune('a'+i)), Label: "reject",
			Tier: "silver", PublishedAt: base.AddDate(0, 0, i), ClusterID: "c-silver-" + string(rune('a'+i)),
		})
	}
	// A silver example sharing a cluster with a gold test example must
	// not reach train either: that would leak the test event.
	leakCluster := "c-test-event"
	for i, ex := range withSilver.Examples {
		if ex.ArticleID == wantTest.Examples[0].ArticleID {
			withSilver.Examples[i].ClusterID = leakCluster
		}
	}
	withSilver.Examples = append(withSilver.Examples, editorialtrain.TrainingExample{
		ArticleID: "s-leak", Title: "silver leak", Label: "publish", Tier: "silver",
		PublishedAt: base, ClusterID: leakCluster,
	})

	train, validate, test := editorialtrain.SplitForTraining(withSilver, defaultTrainConfig())
	if len(validate.Examples) != len(wantValidate.Examples) || len(test.Examples) != len(wantTest.Examples) {
		t.Fatalf("validate/test = %d/%d, want gold-only %d/%d", len(validate.Examples), len(test.Examples), len(wantValidate.Examples), len(wantTest.Examples))
	}
	for _, part := range []editorialtrain.Dataset{validate, test} {
		for _, ex := range part.Examples {
			if ex.Tier == "silver" {
				t.Errorf("silver example %s in validate/test", ex.ArticleID)
			}
		}
	}
	if got, want := len(train.Examples), len(wantTrain.Examples)+10; got != want {
		t.Errorf("train = %d examples, want %d (gold train + 10 silver, leak excluded)", got, want)
	}
	for _, ex := range train.Examples {
		if ex.ArticleID == "s-leak" {
			t.Error("silver example sharing a test cluster reached train")
		}
	}
}
