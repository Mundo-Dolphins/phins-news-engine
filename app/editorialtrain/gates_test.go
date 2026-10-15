package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func passingConfig() editorialtrain.GateConfig {
	return editorialtrain.GateConfig{
		MinPrecision: 0.7, MaxRecallRegression: 0.05, MinPRAUC: 0.5,
		MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: 5,
		MaxArtifactSizeBytes: 1_048_576, MaxInferenceNanos: 1_000_000,
	}
}

func passingReport() editorialtrain.Report {
	return editorialtrain.Report{
		Overall: editorialtrain.BinaryMetrics{TP: 90, FP: 5, FN: 5, TN: 900},
		PRAUC:   0.9, InferenceNanos: 100,
	}
}

func TestEvaluateGates_AllPassing(t *testing.T) {
	result := editorialtrain.EvaluateGates(passingReport(), nil, 1000, passingConfig())
	if !result.Passed || len(result.Failures) != 0 {
		t.Errorf("EvaluateGates() = %+v, want Passed=true, no failures", result)
	}
}

func TestEvaluateGates_LowPrecisionFails(t *testing.T) {
	report := passingReport()
	report.Overall = editorialtrain.BinaryMetrics{TP: 10, FP: 20, FN: 5, TN: 895} // precision = 10/30 ≈ 0.33; FP rate = 20/915 ≈ 0.022 (under 0.5) so only the precision gate trips
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false for low precision")
	}
	if len(result.Failures) != 1 || !containsSubstring(result.Failures[0], "precision") {
		t.Errorf("Failures = %v, want exactly one mentioning precision", result.Failures)
	}
}

func TestEvaluateGates_LowPRAUCFails(t *testing.T) {
	report := passingReport()
	report.PRAUC = 0.1
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false for low PR-AUC")
	}
}

func TestEvaluateGates_HighFalsePositiveRateFails(t *testing.T) {
	report := passingReport()
	report.Overall = editorialtrain.BinaryMetrics{TP: 1200, FP: 480, FN: 5, TN: 20} // precision = 1200/1680 ≈ 0.714 > 0.7; FP rate = 480/(480+20) = 0.96
	cfg := passingConfig()
	result := editorialtrain.EvaluateGates(report, nil, 1000, cfg)
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false when FP rate exceeds MaxFalsePositiveRate")
	}
	if len(result.Failures) != 1 || !containsSubstring(result.Failures[0], "false positive rate") {
		t.Errorf("Failures = %v, want exactly one mentioning false positive rate", result.Failures)
	}
}

func TestEvaluateGates_FalsePositiveRate_ZeroDenominatorPasses(t *testing.T) {
	report := passingReport()
	report.Overall = editorialtrain.BinaryMetrics{TP: 90, FP: 0, FN: 5, TN: 0} // no negatives at all in this corpus
	cfg := passingConfig()
	result := editorialtrain.EvaluateGates(report, nil, 1000, cfg)
	if !result.Passed {
		t.Errorf("EvaluateGates() = %+v, want Passed=true when FP+TN=0 (rate undefined, must not spuriously fail)", result)
	}
}

func TestEvaluateGates_ArtifactTooLargeFails(t *testing.T) {
	result := editorialtrain.EvaluateGates(passingReport(), nil, 10_000_000, passingConfig())
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false when artifact size exceeds the max")
	}
}

func TestEvaluateGates_InferenceTooSlowFails(t *testing.T) {
	report := passingReport()
	report.InferenceNanos = 10_000_000
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false when inference time exceeds the max")
	}
}

func TestEvaluateGates_NoPromotedModel_SkipsRecallRegressionCheck(t *testing.T) {
	report := passingReport()
	report.Overall = editorialtrain.BinaryMetrics{TP: 1, FP: 0, FN: 99, TN: 900} // recall ~0.01, would badly fail regression if checked
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	// precision here is 1/(1+0)=1.0, PRAUC/FP/size/inference all still pass — only
	// the (skipped, since promoted==nil) recall-regression check would ever fail.
	if !result.Passed {
		t.Errorf("EvaluateGates() with promoted=nil = %+v, want Passed=true (regression check skipped)", result)
	}
}

func TestEvaluateGates_RecallRegressionAgainstPromotedFails(t *testing.T) {
	promoted := passingReport() // recall = 90/(90+5) ≈ 0.947
	candidate := passingReport()
	candidate.Overall = editorialtrain.BinaryMetrics{TP: 10, FP: 5, FN: 85, TN: 900} // recall ≈ 0.105 — big regression
	result := editorialtrain.EvaluateGates(candidate, &promoted, 1000, passingConfig())
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false for a large recall regression vs. promoted")
	}
}

func TestEvaluateGates_PerSourceLowPrecisionFailsWhenSampleSizeMet(t *testing.T) {
	report := passingReport()
	report.BySource = map[string]editorialtrain.BinaryMetrics{
		"noisy-source": {TP: 1, FP: 9, FN: 0, TN: 0}, // precision 0.1, n=10 >= MinSourceExamples(5)
	}
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false for a low-precision source with enough samples")
	}
}

func TestEvaluateGates_PerSourceLowPrecisionSkippedWhenSampleSizeTooSmall(t *testing.T) {
	report := passingReport()
	report.BySource = map[string]editorialtrain.BinaryMetrics{
		"tiny-source": {TP: 0, FP: 1, FN: 0, TN: 0}, // precision 0, n=1 < MinSourceExamples(5)
	}
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	if !result.Passed {
		t.Errorf("EvaluateGates() = %+v, want Passed=true (source sample size below floor must be skipped, not failed)", result)
	}
}

func TestEvaluateGates_PerSourceWithNoPredictedPublishesIsSkipped(t *testing.T) {
	report := passingReport()
	if report.BySource == nil {
		report.BySource = map[string]editorialtrain.BinaryMetrics{}
	}
	report.BySource["source-no-accepts"] = editorialtrain.BinaryMetrics{FN: 20, TN: 100}
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	if !result.Passed {
		t.Errorf("EvaluateGates() = %+v, want pass: precision is undefined for a source with zero predicted publishes", result)
	}
}

func TestEvaluateGates_MultipleFailuresAllReported(t *testing.T) {
	report := passingReport()
	report.PRAUC = 0.0
	report.InferenceNanos = 10_000_000
	result := editorialtrain.EvaluateGates(report, nil, 1000, passingConfig())
	if len(result.Failures) != 2 {
		t.Errorf("len(Failures) = %d, want 2 (both PR-AUC and inference-time failures reported, not just the first)", len(result.Failures))
	}
}

func TestEvaluateGates_SkipsRecallRegressionWhenPromotedBaselineTooSmall(t *testing.T) {
	promoted := passingReport()
	promoted.Overall = editorialtrain.BinaryMetrics{TP: 3, FP: 1, FN: 0, TN: 0} // 4 examples total, recall 1.000
	candidate := passingReport()
	candidate.Overall = editorialtrain.BinaryMetrics{TP: 10, FP: 4, FN: 85, TN: 900} // precision ~0.714, recall ~0.105, would fail regression if checked

	cfg := passingConfig()
	cfg.MinRegressionBaselineExamples = 500
	result := editorialtrain.EvaluateGates(candidate, &promoted, 1000, cfg)
	if !result.Passed {
		t.Errorf("EvaluateGates() = %+v, want Passed=true (promoted baseline has only 4 examples, below MinRegressionBaselineExamples=500)", result)
	}
}

func TestEvaluateGates_AppliesRecallRegressionWhenPromotedBaselineLargeEnough(t *testing.T) {
	promoted := passingReport()
	promoted.Overall = editorialtrain.BinaryMetrics{TP: 900, FP: 50, FN: 50, TN: 4000} // 5000 examples, recall ~0.947
	candidate := passingReport()
	candidate.Overall = editorialtrain.BinaryMetrics{TP: 10, FP: 4, FN: 85, TN: 900} // precision ~0.714, recall ~0.105 — big regression

	cfg := passingConfig()
	cfg.MinRegressionBaselineExamples = 500
	result := editorialtrain.EvaluateGates(candidate, &promoted, 1000, cfg)
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false: promoted baseline has 5000 examples (>= 500), regression check must apply")
	}
}

func TestEvaluateGates_ExcludedSourceSkipsPerSourceCheck(t *testing.T) {
	report := passingReport()
	report.BySource = map[string]editorialtrain.BinaryMetrics{
		"twitter:lv1475d51980fc9": {TP: 13, FP: 10, FN: 1, TN: 1}, // precision 0.565, would fail 0.6 floor
	}
	cfg := passingConfig()
	cfg.ExcludedSources = []string{"twitter:lv1475d51980fc9"}
	result := editorialtrain.EvaluateGates(report, nil, 1000, cfg)
	if !result.Passed {
		t.Errorf("EvaluateGates() = %+v, want Passed=true: source is in ExcludedSources", result)
	}
}

func TestEvaluateGates_NonExcludedNoisySourceStillFails(t *testing.T) {
	report := passingReport()
	report.BySource = map[string]editorialtrain.BinaryMetrics{
		"twitter:lv1475d51980fc9": {TP: 13, FP: 10, FN: 1, TN: 1},
	}
	cfg := passingConfig() // ExcludedSources empty
	result := editorialtrain.EvaluateGates(report, nil, 1000, cfg)
	if result.Passed {
		t.Fatal("EvaluateGates() Passed = true, want false: source not excluded, precision 0.565 < 0.6")
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > len(substr) && stringsContains(s, substr)))
}

func stringsContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
