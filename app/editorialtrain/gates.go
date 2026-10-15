// app/editorialtrain/gates.go
package editorialtrain

import (
	"fmt"
	"sort"
)

// GateConfig is the configurable quality bar a trained candidate must
// clear before a human can promote it. It
// lives here rather than domain/editorial: Report (what gates evaluate)
// is defined in this package, not domain/editorial, which only knows
// Model/Predict/Thresholds/Zone.
type GateConfig struct {
	MinPrecision        float64
	MaxRecallRegression float64
	MinPRAUC            float64
	// MaxFalsePositiveRate is FP / (FP + TN) — the share of true-negative
	// examples the candidate incorrectly accepted. A rate, not an
	// absolute count, so the gate stays meaningful as the corpus grows
	// (an absolute cap tuned for a 9-example toy set is meaningless
	// against a 1,400-row real test split — see
	// EDITORIAL-CLASSIFIER-GOVERNANCE-REPORT.md §4-5).
	MaxFalsePositiveRate  float64
	MinPerSourcePrecision float64
	MinSourceExamples     int
	// MinRegressionBaselineExamples is the minimum total example count
	// (TP+FP+FN+TN) the currently-promoted model's own Test report must
	// have for the recall-regression check to apply at all. A promoted
	// model trained on too few examples (e.g. a synthetic seed set) can
	// report a spuriously perfect recall from memorization rather than
	// generalization — using that as a regression floor would penalize
	// every real-data candidate for not reproducing an overfitting
	// artifact. Below this floor, the check is skipped entirely (never
	// failed) — see EDITORIAL-CLASSIFIER-GOVERNANCE-REPORT.md §4.
	MinRegressionBaselineExamples int
	MaxArtifactSizeBytes          int64
	MaxInferenceNanos             int64
	// ExcludedSources lists source IDs deliberately excluded from the
	// per-source precision gate — a small, explicit, human-approved
	// allowlist (not the sourcepolicy/source-policies.yml mechanism,
	// which governs dedup/publication authority, not gate evaluation).
	// Each entry must be justified in EDITORIAL-CLASSIFIER-GOVERNANCE-REPORT.md.
	ExcludedSources []string
}

// GateResult is the outcome of evaluating one candidate against a
// GateConfig. Failures is empty when Passed is true.
type GateResult struct {
	Passed   bool
	Failures []string
}

// EvaluateGates checks candidate against cfg. promoted is the currently
// promoted model's own Test report, or nil if there is no promoted model
// yet (e.g. the very first promotion) — the recall-regression check is
// skipped, never failed, when promoted is nil. artifactSizeBytes is
// supplied by the caller (this function has no file-system access). Every
// failing gate produces one specific, human-readable string; a caller
// must never have to guess why a candidate was excluded.
func EvaluateGates(candidate Report, promoted *Report, artifactSizeBytes int64, cfg GateConfig) GateResult {
	var failures []string

	if p := candidate.Overall.Precision(); p < cfg.MinPrecision {
		failures = append(failures, fmt.Sprintf("precision %.3f < min %.3f", p, cfg.MinPrecision))
	}
	if candidate.PRAUC < cfg.MinPRAUC {
		failures = append(failures, fmt.Sprintf("PR-AUC %.3f < min %.3f", candidate.PRAUC, cfg.MinPRAUC))
	}
	if denom := candidate.Overall.FP + candidate.Overall.TN; denom > 0 {
		rate := float64(candidate.Overall.FP) / float64(denom)
		if rate > cfg.MaxFalsePositiveRate {
			failures = append(failures, fmt.Sprintf("false positive rate %.3f > max %.3f (FP=%d, TN=%d)",
				rate, cfg.MaxFalsePositiveRate, candidate.Overall.FP, candidate.Overall.TN))
		}
	}
	if artifactSizeBytes > cfg.MaxArtifactSizeBytes {
		failures = append(failures, fmt.Sprintf("artifact size %d bytes > max %d bytes", artifactSizeBytes, cfg.MaxArtifactSizeBytes))
	}
	if candidate.InferenceNanos > cfg.MaxInferenceNanos {
		failures = append(failures, fmt.Sprintf("inference time %dns > max %dns", candidate.InferenceNanos, cfg.MaxInferenceNanos))
	}

	if promoted != nil {
		promotedTotal := promoted.Overall.TP + promoted.Overall.FP + promoted.Overall.FN + promoted.Overall.TN
		if promotedTotal >= cfg.MinRegressionBaselineExamples {
			regression := promoted.Overall.Recall() - candidate.Overall.Recall()
			if regression > cfg.MaxRecallRegression {
				failures = append(failures, fmt.Sprintf(
					"recall regression %.3f > max %.3f (promoted %.3f, candidate %.3f)",
					regression, cfg.MaxRecallRegression, promoted.Overall.Recall(), candidate.Overall.Recall()))
			}
		}
	}

	excluded := make(map[string]bool, len(cfg.ExcludedSources))
	for _, s := range cfg.ExcludedSources {
		excluded[s] = true
	}
	var sourceFailures []string
	for source, m := range candidate.BySource {
		if excluded[source] {
			continue
		}
		total := m.TP + m.FP + m.FN + m.TN
		if total < cfg.MinSourceExamples {
			continue // too few samples for this source to gate on — neither passed nor failed, just skipped
		}
		if m.TP+m.FP == 0 {
			continue // precision is undefined when the candidate accepted no examples from this source
		}
		if p := m.Precision(); p < cfg.MinPerSourcePrecision {
			sourceFailures = append(sourceFailures, fmt.Sprintf(
				"source %q precision %.3f < min %.3f (n=%d)", source, p, cfg.MinPerSourcePrecision, total))
		}
	}
	sort.Strings(sourceFailures) // BySource iteration order is nondeterministic; sort for stable output
	failures = append(failures, sourceFailures...)

	return GateResult{Passed: len(failures) == 0, Failures: failures}
}
