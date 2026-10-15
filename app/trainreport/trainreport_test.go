package trainreport_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainreport"
)

func TestRender_Promoted(t *testing.T) {
	in := trainreport.Input{
		RunAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC), Auto: false,
		TotalExamples: 1204, GoldCount: 912, SilverCount: 292, DatasetHash: "a1b2c3",
		ProposedCount: 292, AutoLabeledCount: 18, SentToReviewCount: 274,
		QueueSize: 41, QueueDisagreementCount: 12, QueueNovelVocabularyCount: 9, QueueOtherCount: 20,
		DisagreementCount: 23, DisagreementRate: 0.019,
		CandidateVersion: "v14", ProductionVersion: "v13",
		CandidateMetrics:  editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 971, FP: 29, FN: 133, TN: 71}},
		ProductionMetrics: &editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 962, FP: 38, FN: 119, TN: 85}},
		Gates:             editorialtrain.GateResult{Passed: true},
		PromotionGates:    editorialtrain.GateResult{Passed: true},
		Recommendation:    "PROMOTE",
		ResultSummary:     "promoted v14",
	}
	got := trainreport.Render(in)

	for _, want := range []string{
		"Training run 2026-09-22T03:00:00Z (auto=false)",
		"Dataset: 1204 examples (912 Gold, 292 Silver), hash a1b2c3",
		"LLM labels: 292 proposed, 18 auto-labeled, 274 sent to review",
		"Review queue: 41 items (12 disagreement, 9 novel vocabulary, 20 other)",
		"NB/LLM disagreement: 23 examples (1.9%)",
		"Candidate v14 vs. production v13:",
		"Gates: EditorialGates PASS, PromotionConfig PASS",
		"Recommendation: PROMOTE",
		"Result: promoted v14",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRender_RejectedNoBaseline(t *testing.T) {
	in := trainreport.Input{
		RunAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC), Auto: true,
		CandidateVersion: "v1", ProductionVersion: "",
		CandidateMetrics:  editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 10, FP: 20, FN: 5, TN: 5}},
		ProductionMetrics: nil,
		Gates:             editorialtrain.GateResult{Passed: false, Failures: []string{"precision 0.333 < min 0.9"}},
		PromotionGates:    editorialtrain.GateResult{Passed: false, Failures: []string{"promotion: precision 0.333 < min 0.9"}},
		Recommendation:    "REJECT",
		ResultSummary:     "rejected: precision 0.333 < min 0.9; promotion: precision 0.333 < min 0.9",
	}
	got := trainreport.Render(in)

	for _, want := range []string{
		"Training run 2026-09-22T03:00:00Z (auto=true)",
		"Candidate v1 vs. production (none)",
		"Gates: EditorialGates FAIL (precision 0.333 < min 0.9), PromotionConfig FAIL (promotion: precision 0.333 < min 0.9)",
		"Recommendation: REJECT",
		"Result: rejected: precision 0.333 < min 0.9; promotion: precision 0.333 < min 0.9",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestRender_ForcedPromotion covers finding 8: a forced promotion must
// be visible in the persisted audit trail, not merely inferable from a
// PROMOTE recommendation sitting next to a failing gate line.
func TestRender_ForcedPromotion(t *testing.T) {
	in := trainreport.Input{
		RunAt:            time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		CandidateVersion: "v14", ProductionVersion: "v13",
		CandidateMetrics:  editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 10, FP: 2, FN: 1, TN: 7}},
		ProductionMetrics: &editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 11, FP: 1, FN: 1, TN: 7}},
		Gates:             editorialtrain.GateResult{Passed: true},
		PromotionGates:    editorialtrain.GateResult{Passed: false, Failures: []string{"promotion: precision 0.833 < min 0.85"}},
		Forced:            true,
		Recommendation:    "PROMOTE",
		ResultSummary:     "promoted v14",
	}
	got := trainreport.Render(in)

	if !strings.Contains(got, "Promoted via --force override") {
		t.Errorf("Render() missing the --force override notice\nfull output:\n%s", got)
	}
}

// TestRender_ForcedButRejected_HasNoOverrideNotice covers the bug found
// in re-review: --force only bypasses PromotionConfig, never
// EditorialGates. When no candidate clears EditorialGates, a forced run
// still ends in REJECT and nothing was promoted, so the audit trail must
// not falsely claim a forced promotion happened.
func TestRender_ForcedButRejected_HasNoOverrideNotice(t *testing.T) {
	in := trainreport.Input{
		RunAt:            time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		CandidateVersion: "v14", ProductionVersion: "v13",
		CandidateMetrics:  editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 10, FP: 2, FN: 1, TN: 7}},
		ProductionMetrics: &editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 11, FP: 1, FN: 1, TN: 7}},
		Gates:             editorialtrain.GateResult{Passed: false, Failures: []string{"precision 0.833 < min 0.9"}},
		PromotionGates:    editorialtrain.GateResult{Passed: false, Failures: []string{"promotion: precision 0.833 < min 0.85"}},
		Forced:            true,
		Recommendation:    "REJECT",
		ResultSummary:     "rejected: precision 0.833 < min 0.9",
	}
	got := trainreport.Render(in)

	if strings.Contains(got, "Promoted via --force override") {
		t.Errorf("Render() falsely claims a forced promotion on a REJECT outcome\nfull output:\n%s", got)
	}
}

func TestRender_NotForced_HasNoOverrideNotice(t *testing.T) {
	in := trainreport.Input{
		RunAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		Gates: editorialtrain.GateResult{Passed: true}, PromotionGates: editorialtrain.GateResult{Passed: true},
		Recommendation: "PROMOTE", ResultSummary: "promoted v14",
	}
	if got := trainreport.Render(in); strings.Contains(got, "--force") {
		t.Errorf("Render() mentions --force for an unforced run\nfull output:\n%s", got)
	}
}

func TestRender_AutoTriggeredHeaderIncludesReasons(t *testing.T) {
	in := trainreport.Input{
		RunAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC), Auto: true,
		TriggerReasons:   []string{"142 new Gold examples (threshold 100)"},
		CandidateVersion: "v14", ProductionVersion: "v13",
		CandidateMetrics:  editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 90, FP: 10, FN: 10, TN: 890}},
		ProductionMetrics: &editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 85, FP: 15, FN: 15, TN: 885}},
		Gates:             editorialtrain.GateResult{Passed: true},
		PromotionGates:    editorialtrain.GateResult{Passed: true},
		Recommendation:    "PROMOTE", ResultSummary: "promoted v14",
	}
	got := trainreport.Render(in)
	want := "Training run 2026-09-22T03:00:00Z (auto=true, triggered by: 142 new Gold examples (threshold 100))"
	if !strings.Contains(got, want) {
		t.Errorf("Render() missing %q\nfull output:\n%s", want, got)
	}
}

func TestRender_AutoFalseIgnoresTriggerReasons(t *testing.T) {
	in := trainreport.Input{
		RunAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC), Auto: false,
		TriggerReasons:   []string{"should never be rendered when Auto is false"},
		CandidateMetrics: editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 0, FN: 0, TN: 1}},
		Gates:            editorialtrain.GateResult{Passed: true}, PromotionGates: editorialtrain.GateResult{Passed: true},
		Recommendation: "PROMOTE", ResultSummary: "promoted v1",
	}
	got := trainreport.Render(in)
	want := "Training run 2026-09-22T03:00:00Z (auto=false)\n"
	if !strings.Contains(got, want) {
		t.Errorf("Render() = %q, want it to contain %q (TriggerReasons ignored when Auto is false)", got, want)
	}
	if strings.Contains(got, "triggered by") {
		t.Errorf("Render() = %q, must not mention \"triggered by\" when Auto is false", got)
	}
}

func TestRender_UnknownTermsNote(t *testing.T) {
	in := trainreport.Input{
		RunAt:            time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		UnknownTermsNote: "linebacker, practice squad",
		CandidateMetrics: editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 0, FN: 0, TN: 1}},
		Gates:            editorialtrain.GateResult{Passed: true}, PromotionGates: editorialtrain.GateResult{Passed: true},
		Recommendation: "PROMOTE", ResultSummary: "promoted v1",
	}
	got := trainreport.Render(in)
	want := "Unknown terms in hard-positive articles: linebacker, practice squad; run entities discover / dictionary mining to review"
	if !strings.Contains(got, want) {
		t.Errorf("Render() missing %q\nfull output:\n%s", want, got)
	}
}

func TestRender_EmptyUnknownTermsNoteOmitsLine(t *testing.T) {
	in := trainreport.Input{
		RunAt:            time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		CandidateMetrics: editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 0, FN: 0, TN: 1}},
		Gates:            editorialtrain.GateResult{Passed: true}, PromotionGates: editorialtrain.GateResult{Passed: true},
		Recommendation: "PROMOTE", ResultSummary: "promoted v1",
	}
	got := trainreport.Render(in)
	if strings.Contains(got, "Unknown terms") {
		t.Errorf("Render() = %q, must not mention unknown terms when UnknownTermsNote is empty", got)
	}
}

// TestRender_UnresolvedHardExampleCount covers the whole-branch review's
// Finding 4: trainrun.Run read UnresolvedCount and discarded it behind a
// comment claiming it surfaced in the report. It now really does.
func TestRender_UnresolvedHardExampleCount(t *testing.T) {
	in := trainreport.Input{
		RunAt:                      time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		UnresolvedHardExampleCount: 7,
		CandidateMetrics:           editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 0, FN: 0, TN: 1}},
		Gates:                      editorialtrain.GateResult{Passed: true}, PromotionGates: editorialtrain.GateResult{Passed: true},
		Recommendation: "PROMOTE", ResultSummary: "promoted v1",
	}
	got := trainreport.Render(in)
	want := "Hard examples awaiting a human label: 7"
	if !strings.Contains(got, want) {
		t.Errorf("Render() missing %q\nfull output:\n%s", want, got)
	}
}

func TestRender_ZeroUnresolvedHardExampleCountOmitsLine(t *testing.T) {
	in := trainreport.Input{
		RunAt:            time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		CandidateMetrics: editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 0, FN: 0, TN: 1}},
		Gates:            editorialtrain.GateResult{Passed: true}, PromotionGates: editorialtrain.GateResult{Passed: true},
		Recommendation: "PROMOTE", ResultSummary: "promoted v1",
	}
	got := trainreport.Render(in)
	if strings.Contains(got, "Hard examples") {
		t.Errorf("Render() = %q, must not mention hard examples when the count is zero (mining unwired or found nothing)", got)
	}
}

func TestRender_ReportsSourceConcentrationWhenKnown(t *testing.T) {
	got := trainreport.Render(trainreport.Input{
		RunAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC), TotalExamples: 10,
		TopSource: "src-a", TopSourceShare: 0.62,
	})
	if want := "Top source: src-a (62.0% of examples)"; !strings.Contains(got, want) {
		t.Errorf("Render() missing %q\nfull output:\n%s", want, got)
	}
}

func TestRender_OmitsSourceConcentrationWhenUnknown(t *testing.T) {
	got := trainreport.Render(trainreport.Input{RunAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)})
	if strings.Contains(got, "Top source") {
		t.Errorf("Render() mentions Top source with no data:\n%s", got)
	}
}

func TestRender_ReportsReviewRateErrorRatesAndConfusionMatrix(t *testing.T) {
	in := trainreport.Input{
		RunAt:            time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		ManualReviewRate: 0.05,
		CandidateVersion: "v14", ProductionVersion: "v13",
		CandidateMetrics:  editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 90, FP: 10, FN: 10, TN: 90}},
		ProductionMetrics: &editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 80, FP: 20, FN: 20, TN: 80}},
		Recommendation:    "PROMOTE",
	}
	got := trainreport.Render(in)
	for _, want := range []string{
		"manual_review_rate: 5.0%",
		"fpr        0.200 -> 0.100",
		"fnr        0.200 -> 0.100",
		"confusion (candidate): TP=90 FN=10 FP=10 TN=90",
		"confusion (production): TP=80 FN=20 FP=20 TN=80",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRender_NoBaselineStillShowsConfusionMatrix(t *testing.T) {
	in := trainreport.Input{
		CandidateVersion: "v1",
		CandidateMetrics: editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 9, FP: 1, FN: 2, TN: 8}},
		Recommendation:   "REJECT",
	}
	got := trainreport.Render(in)
	if !strings.Contains(got, "confusion (candidate): TP=9 FN=2 FP=1 TN=8") || !strings.Contains(got, "fpr") {
		t.Errorf("missing confusion/fpr without baseline:\n%s", got)
	}
}
