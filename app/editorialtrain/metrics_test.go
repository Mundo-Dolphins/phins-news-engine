package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
)

// perfectModel scores "signal" >= 1 as strongly positive, else strongly
// negative — a hand-built separator, not a Fit() output, so the expected
// confusion matrix is known exactly.
func perfectModel() editorial.Model {
	return editorial.Model{
		Bias: -10, Weights: map[string]float64{"uni:signal": 20},
		Thresholds: editorial.Thresholds{Reject: 30, Accept: 70},
	}
}

func labeledExampleWithSignal(id, label string, hasSignal bool, source, contentType string) editorialtrain.TrainingExample {
	title := "neutral text"
	if hasSignal {
		title = "signal"
	}
	return editorialtrain.TrainingExample{ArticleID: id, Title: title, Label: label, SourceID: source, ContentType: contentType}
}

func TestEvaluate_PerfectSeparatorConfusionMatrix(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		labeledExampleWithSignal("p1", "publish", true, "src-a", "article"),
		labeledExampleWithSignal("p2", "publish", true, "src-a", "article"),
		labeledExampleWithSignal("r1", "reject", false, "src-b", "social_post"),
		labeledExampleWithSignal("r2", "reject", false, "src-b", "social_post"),
	}
	report := editorialtrain.Evaluate(perfectModel(), examples, nil)

	if report.Overall.TP != 2 || report.Overall.TN != 2 || report.Overall.FP != 0 || report.Overall.FN != 0 {
		t.Errorf("Overall = %+v, want TP=2 TN=2 FP=0 FN=0", report.Overall)
	}
	if report.Overall.Precision() != 1 || report.Overall.Recall() != 1 || report.Overall.F1() != 1 {
		t.Errorf("Precision/Recall/F1 = %v/%v/%v, want 1/1/1", report.Overall.Precision(), report.Overall.Recall(), report.Overall.F1())
	}
	if report.PRAUC < 0.99 {
		t.Errorf("PRAUC = %v, want ~1.0 for a perfect separator", report.PRAUC)
	}
	if len(report.FalsePositives) != 0 || len(report.FalseNegatives) != 0 {
		t.Errorf("expected no false positives/negatives, got %v / %v", report.FalsePositives, report.FalseNegatives)
	}
}

func TestEvaluate_MisclassificationsCounted(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		labeledExampleWithSignal("fp1", "reject", true, "src-a", "article"),   // model says publish, gold says reject
		labeledExampleWithSignal("fn1", "publish", false, "src-a", "article"), // model says reject, gold says publish
	}
	report := editorialtrain.Evaluate(perfectModel(), examples, nil)

	if report.Overall.FP != 1 || report.Overall.FN != 1 {
		t.Errorf("Overall = %+v, want FP=1 FN=1", report.Overall)
	}
	if len(report.FalsePositives) != 1 || report.FalsePositives[0] != "fp1" {
		t.Errorf("FalsePositives = %v, want [fp1]", report.FalsePositives)
	}
	if len(report.FalseNegatives) != 1 || report.FalseNegatives[0] != "fn1" {
		t.Errorf("FalseNegatives = %v, want [fn1]", report.FalseNegatives)
	}
}

func TestEvaluate_BySourceAndByContentType(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		labeledExampleWithSignal("p1", "publish", true, "src-a", "article"),
		labeledExampleWithSignal("r1", "reject", false, "src-b", "social_post"),
	}
	report := editorialtrain.Evaluate(perfectModel(), examples, nil)

	if _, ok := report.BySource["src-a"]; !ok {
		t.Error("expected BySource to have an entry for src-a")
	}
	if _, ok := report.ByContentType["social_post"]; !ok {
		t.Error("expected ByContentType to have an entry for social_post")
	}
}

func TestEvaluate_ModelSizeAndInferenceAreNonZero(t *testing.T) {
	report := editorialtrain.Evaluate(perfectModel(), []editorialtrain.TrainingExample{labeledExampleWithSignal("p1", "publish", true, "s", "article")}, nil)
	if report.ModelSizeBytes <= 0 {
		t.Error("expected ModelSizeBytes > 0")
	}
	if report.InferenceNanos <= 0 {
		t.Error("expected InferenceNanos > 0")
	}
	if report.MemoryBytesApprox <= 0 {
		t.Error("expected MemoryBytesApprox > 0")
	}
}

func TestEvaluate_CalibrationBucketsCoverFullRange(t *testing.T) {
	report := editorialtrain.Evaluate(perfectModel(), []editorialtrain.TrainingExample{
		labeledExampleWithSignal("p1", "publish", true, "s", "article"),
		labeledExampleWithSignal("r1", "reject", false, "s", "article"),
	}, nil)
	if len(report.Calibration) == 0 {
		t.Error("expected at least one calibration bucket")
	}
	total := 0
	for _, b := range report.Calibration {
		total += b.Count
	}
	if total != 2 {
		t.Errorf("calibration buckets cover %d examples, want 2", total)
	}
}

func TestEvaluate_VetoImpactIsolatesWhatTheModelAloneWouldHaveMissed(t *testing.T) {
	// perfectModel accepts everything with "good" in the title, rejects
	// everything else — see this file's existing perfectModel/perfectExamples
	// helpers for the established fixture convention; reuse them if present,
	// otherwise define a small local model+examples pair like:
	m := editorial.Model{Bias: -5, Weights: map[string]float64{"uni:good": 10}, Thresholds: editorial.Thresholds{Reject: 40, Accept: 60}}
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", Title: "good isotopes news", Body: "", Label: "publish"},
		{ArticleID: "a2", Title: "good but has free bets promo", Body: "free bets available", Label: "reject"},
		{ArticleID: "a3", Title: "bad spam free bets", Body: "free bets available", Label: "reject"},
	}
	vetoCfg := &editorialveto.Config{
		Version: "test",
		Rules:   []editorialveto.Rule{{Category: editorialveto.CategoryGambling, ReasonCode: "veto:gambling:x", Patterns: []string{`\bfree bets\b`}}},
	}

	report := editorialtrain.Evaluate(m, examples, vetoCfg)

	if report.VetoImpact == nil {
		t.Fatal("VetoImpact = nil, want non-nil when vetoCfg is provided")
	}
	// a2: veto triggers (has "free bets"), model scores it "good" -> accept
	// zone (model would have missed it). a3: veto triggers, model also
	// would reject it (no "good"). a1: veto never triggers.
	if report.VetoImpact.Triggered != 2 {
		t.Errorf("Triggered = %d, want 2", report.VetoImpact.Triggered)
	}
	if report.VetoImpact.TriggeredModelWouldAccept != 1 {
		t.Errorf("TriggeredModelWouldAccept = %d, want 1 (a2)", report.VetoImpact.TriggeredModelWouldAccept)
	}
	if report.VetoImpact.TriggeredModelWouldReject != 1 {
		t.Errorf("TriggeredModelWouldReject = %d, want 1 (a3)", report.VetoImpact.TriggeredModelWouldReject)
	}
	if report.VetoImpact.NotTriggered != 1 {
		t.Errorf("NotTriggered = %d, want 1 (a1)", report.VetoImpact.NotTriggered)
	}
}

func TestEvaluate_NilVetoConfigLeavesVetoImpactNil(t *testing.T) {
	m := editorial.Model{Bias: 0, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 40, Accept: 60}}
	report := editorialtrain.Evaluate(m, []editorialtrain.TrainingExample{{ArticleID: "a1", Label: "publish"}}, nil)
	if report.VetoImpact != nil {
		t.Errorf("VetoImpact = %+v, want nil when vetoCfg is nil", report.VetoImpact)
	}
}

func TestEvaluate_ManualReviewRate(t *testing.T) {
	// perfectModel (Reject: 30, Accept: 70) never lands in the ambiguous
	// zone for these examples, so the baseline case is 0.
	examples := []editorialtrain.TrainingExample{
		labeledExampleWithSignal("p1", "publish", true, "src-a", "article"),
		labeledExampleWithSignal("r1", "reject", false, "src-b", "social_post"),
	}
	report := editorialtrain.Evaluate(perfectModel(), examples, nil)
	if report.ManualReviewRate != 0 {
		t.Errorf("ManualReviewRate = %v, want 0 (perfectModel never lands ambiguous)", report.ManualReviewRate)
	}

	// A model whose weight for "uni:signal" is 0 collapses every example
	// to the same raw score (bias 0 -> Value 50), which sits strictly
	// between Reject=30 and Accept=70 -> ZoneAmbiguous for all 4.
	ambiguousModel := editorial.Model{
		Bias: 0, Weights: map[string]float64{"uni:signal": 0},
		Thresholds: editorial.Thresholds{Reject: 30, Accept: 70},
	}
	mixed := []editorialtrain.TrainingExample{
		labeledExampleWithSignal("p1", "publish", true, "src-a", "article"),
		labeledExampleWithSignal("p2", "publish", true, "src-a", "article"),
		labeledExampleWithSignal("r1", "reject", false, "src-b", "social_post"),
		labeledExampleWithSignal("r2", "reject", false, "src-b", "social_post"),
	}
	report = editorialtrain.Evaluate(ambiguousModel, mixed, nil)
	if report.ManualReviewRate != 1 {
		t.Errorf("ManualReviewRate = %v, want 1 (all 4 examples ambiguous)", report.ManualReviewRate)
	}
}

func TestEvaluate_AmbiguousPolicyMatchesEnforceBehavior(t *testing.T) {
	model := editorial.Model{Bias: 0, Thresholds: editorial.Thresholds{Reject: 30, Accept: 70}}
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "p1", Label: "publish", SourceID: "src", Title: "neutral"},
		{ArticleID: "r1", Label: "reject", SourceID: "src", Title: "neutral"},
	}

	accept := editorialtrain.Evaluate(model, examples, nil, "accept")
	if accept.Overall.TP != 1 || accept.Overall.FP != 1 || accept.Overall.TN != 0 || accept.Overall.FN != 0 {
		t.Errorf("accept policy metrics = %+v, want TP=1 FP=1", accept.Overall)
	}
	reject := editorialtrain.Evaluate(model, examples, nil, "reject")
	if reject.Overall.TP != 0 || reject.Overall.FP != 0 || reject.Overall.TN != 1 || reject.Overall.FN != 1 {
		t.Errorf("reject policy metrics = %+v, want TN=1 FN=1", reject.Overall)
	}
}

func TestEvaluate_ManualReviewRate_EmptyExamples(t *testing.T) {
	report := editorialtrain.Evaluate(perfectModel(), nil, nil)
	if report.ManualReviewRate != 0 {
		t.Errorf("ManualReviewRate = %v, want 0 for empty examples (no division by zero)", report.ManualReviewRate)
	}
}

func TestBinaryMetrics_ErrorRates(t *testing.T) {
	m := editorialtrain.BinaryMetrics{TP: 6, FP: 1, FN: 2, TN: 9}
	if got := m.FalsePositiveRate(); got != 0.1 {
		t.Errorf("FalsePositiveRate() = %v, want 0.1", got)
	}
	if got := m.FalseNegativeRate(); got != 0.25 {
		t.Errorf("FalseNegativeRate() = %v, want 0.25", got)
	}
	var zero editorialtrain.BinaryMetrics
	if zero.FalsePositiveRate() != 0 || zero.FalseNegativeRate() != 0 {
		t.Error("zero-denominator rates must be 0")
	}
}
