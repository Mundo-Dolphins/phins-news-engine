// Package trainreport renders `train run`'s textual report as a pure function over the four pipeline stages' already-
// computed outputs — no I/O, fully unit-testable.
package trainreport

import (
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

// Input is everything Render needs, gathered by the train run
// orchestrator (app/trainrun, Task 9) from its build,
// analyze, evaluate, and promote stages.
type Input struct {
	RunAt time.Time
	Auto  bool
	// TriggerReasons is non-empty only for a triggered `train run
	// --auto` invocation -- the retraintrigger.Result.Reasons that
	// fired. Rendered into the header line when non-empty; ignored
	// otherwise (including every non-auto run, where it is always nil).
	TriggerReasons []string

	TotalExamples int
	GoldCount     int
	SilverCount   int
	DatasetHash   string
	// TopSource/TopSourceShare report how concentrated the dataset is in
	// one source (editorialtrain.SourceConcentration); omitted from the
	// report when TopSource is "".
	TopSource      string
	TopSourceShare float64

	ProposedCount             int
	AutoLabeledCount          int
	SentToReviewCount         int
	ManualReviewRate          float64
	QueueSize                 int
	QueueDisagreementCount    int
	QueueNovelVocabularyCount int
	QueueOtherCount           int

	DisagreementCount int
	DisagreementRate  float64

	CandidateVersion  string
	ProductionVersion string // "" when there is no promoted model yet
	CandidateMetrics  editorialtrain.Report
	ProductionMetrics *editorialtrain.Report // nil when there is no promoted model yet

	Gates          editorialtrain.GateResult // EditorialGates result
	PromotionGates editorialtrain.GateResult // PromotionConfig result

	// UnknownTermsNote is a report-only nudge: a
	// comma-joined list of distinct words from hard-positive articles'
	// titles absent from the current dictionary/entity catalog, or ""
	// when mining found nothing or was not run.
	UnknownTermsNote string

	// UnresolvedHardExampleCount is how many hard_example_candidates rows
	// still have no resolved_at at the end of the mining stage — the same
	// number application/retraintrigger's MinHardExampleCount trigger
	// reads. Rendered only when mining actually ran (i.e. > 0), so a run
	// with mining unwired reports nothing about it.
	UnresolvedHardExampleCount int

	// Forced records that the run was invoked with --force, i.e. a human
	// explicitly allowed promotion despite the offline_training.promotion
	// criteria failing. The persisted report is the audit trail, so this
	// must be visible in it (editorial_gates are never bypassable, with
	// or without --force).
	Forced bool

	Recommendation string // "PROMOTE" | "REJECT"
	ResultSummary  string // e.g. "promoted v14" or "rejected: <reasons>"
}

// Render formats in as the textual report every `train run` invocation
// prints to stdout and persists via training.Run.Report.
func Render(in Input) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Training run %s (auto=%v%s)\n", in.RunAt.UTC().Format(time.RFC3339), in.Auto, triggerSuffix(in.Auto, in.TriggerReasons))
	fmt.Fprintf(&b, "  Dataset: %d examples (%d Gold, %d Silver), hash %s\n", in.TotalExamples, in.GoldCount, in.SilverCount, in.DatasetHash)
	if in.TopSource != "" {
		fmt.Fprintf(&b, "  Top source: %s (%.1f%% of examples)\n", in.TopSource, in.TopSourceShare*100)
	}
	fmt.Fprintf(&b, "  LLM labels: %d proposed, %d auto-labeled, %d sent to review\n", in.ProposedCount, in.AutoLabeledCount, in.SentToReviewCount)
	fmt.Fprintf(&b, "  manual_review_rate: %.1f%% (LLM proposals below the auto-label threshold)\n", in.ManualReviewRate*100)
	fmt.Fprintf(&b, "  Review queue: %d items (%d disagreement, %d novel vocabulary, %d other)\n",
		in.QueueSize, in.QueueDisagreementCount, in.QueueNovelVocabularyCount, in.QueueOtherCount)
	fmt.Fprintf(&b, "  NB/LLM disagreement: %d examples (%.1f%%)\n", in.DisagreementCount, in.DisagreementRate*100)

	production := in.ProductionVersion
	if production == "" {
		production = "(none)"
	}
	fmt.Fprintf(&b, "  Candidate %s vs. production %s:\n", in.CandidateVersion, production)
	fmt.Fprintf(&b, "%s", renderMetricsComparison(in.CandidateMetrics, in.ProductionMetrics))

	fmt.Fprintf(&b, "  Gates: EditorialGates %s, PromotionConfig %s\n", gateStatus(in.Gates), gateStatus(in.PromotionGates))
	if in.Forced && in.Recommendation == "PROMOTE" && (!in.Gates.Passed || !in.PromotionGates.Passed) {
		fmt.Fprintf(&b, "  ⚠ Promoted via --force override: the promotion criteria did not pass and a human bypassed them\n")
	}
	fmt.Fprintf(&b, "  Recommendation: %s\n", in.Recommendation)
	fmt.Fprintf(&b, "  Result: %s\n", in.ResultSummary)
	if in.UnresolvedHardExampleCount > 0 {
		fmt.Fprintf(&b, "  Hard examples awaiting a human label: %d; review them with `editorial review`\n", in.UnresolvedHardExampleCount)
	}
	if in.UnknownTermsNote != "" {
		fmt.Fprintf(&b, "  Unknown terms in hard-positive articles: %s; run entities discover / dictionary mining to review\n", in.UnknownTermsNote)
	}

	return b.String()
}

// triggerSuffix renders ", triggered by: <reasons>" for a triggered
// auto run, or "" otherwise (including every non-auto run, where
// reasons is always empty regardless of what a caller passes).
func triggerSuffix(auto bool, reasons []string) string {
	if !auto || len(reasons) == 0 {
		return ""
	}
	return ", triggered by: " + strings.Join(reasons, ", ")
}

func gateStatus(g editorialtrain.GateResult) string {
	if g.Passed {
		return "PASS"
	}
	return fmt.Sprintf("FAIL (%s)", strings.Join(g.Failures, "; "))
}

func renderMetricsComparison(candidate editorialtrain.Report, production *editorialtrain.Report) string {
	var b strings.Builder
	if production == nil {
		fmt.Fprintf(&b, "    precision  %.3f (no production baseline)\n", candidate.Overall.Precision())
		fmt.Fprintf(&b, "    recall     %.3f (no production baseline)\n", candidate.Overall.Recall())
		fmt.Fprintf(&b, "    f1         %.3f (no production baseline)\n", candidate.Overall.F1())
		fmt.Fprintf(&b, "    fpr        %.3f (no production baseline)\n", candidate.Overall.FalsePositiveRate())
		fmt.Fprintf(&b, "    fnr        %.3f (no production baseline)\n", candidate.Overall.FalseNegativeRate())
		fmt.Fprintf(&b, "    %s\n", confusionLine("candidate", candidate.Overall))
		fmt.Fprintf(&b, "    model manual-review rate %.1f%% (no production baseline)\n", candidate.ManualReviewRate*100)
		return b.String()
	}
	fmt.Fprintf(&b, "    precision  %.3f -> %.3f  (%+.3f)\n", production.Overall.Precision(), candidate.Overall.Precision(), candidate.Overall.Precision()-production.Overall.Precision())
	fmt.Fprintf(&b, "    recall     %.3f -> %.3f  (%+.3f)\n", production.Overall.Recall(), candidate.Overall.Recall(), candidate.Overall.Recall()-production.Overall.Recall())
	fmt.Fprintf(&b, "    f1         %.3f -> %.3f  (%+.3f)\n", production.Overall.F1(), candidate.Overall.F1(), candidate.Overall.F1()-production.Overall.F1())
	fmt.Fprintf(&b, "    fpr        %.3f -> %.3f  (%+.3f)\n", production.Overall.FalsePositiveRate(), candidate.Overall.FalsePositiveRate(), candidate.Overall.FalsePositiveRate()-production.Overall.FalsePositiveRate())
	fmt.Fprintf(&b, "    fnr        %.3f -> %.3f  (%+.3f)\n", production.Overall.FalseNegativeRate(), candidate.Overall.FalseNegativeRate(), candidate.Overall.FalseNegativeRate()-production.Overall.FalseNegativeRate())
	fmt.Fprintf(&b, "    %s\n", confusionLine("candidate", candidate.Overall))
	fmt.Fprintf(&b, "    %s\n", confusionLine("production", production.Overall))
	fmt.Fprintf(&b, "    model manual-review rate %.1f%% -> %.1f%%\n", production.ManualReviewRate*100, candidate.ManualReviewRate*100)
	return b.String()
}

// confusionLine renders the publish-positive confusion matrix on one line:
// rows are actual (publish: TP, FN; discard: FP, TN).
func confusionLine(name string, m editorialtrain.BinaryMetrics) string {
	return fmt.Sprintf("confusion (%s): TP=%d FN=%d FP=%d TN=%d", name, m.TP, m.FN, m.FP, m.TN)
}
