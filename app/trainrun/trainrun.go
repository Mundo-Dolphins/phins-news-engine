// Package trainrun orchestrates `train run`'s full safe-promotion
// workflow: build the dataset, analyze the review
// queue/LLM labels, evaluate every candidate algorithm, and promote the
// best one if it clears both gate sets -- aborting before any later
// stage, with the production model file and every *promoted*
// model_registry row untouched, if an earlier stage fails (the
// "Ollama unavailable" case: build/analyze are the stages
// that call the LLM teacher). See Run's doc comment for the precise
// scope of that guarantee.
package trainrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainanalyze"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainmine"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainreport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

// queueLimit is the bound Run passes to trainanalyze.Analyze's queue
// read -- high enough to be the true total for any realistic review
// queue, while still never issuing an unbounded query.
const queueLimit = 100000

// Deps are the ports Run writes through and reads from.
type Deps struct {
	Registry  out.ModelRegistryRepository
	RunRepo   out.TrainingRunRepository
	UOW       out.UnitOfWork
	Labels    out.HumanLabelRepository
	Articles  out.ArticleRepository
	Proposals editoriallabelproposalport.EditorialLabelProposalRepository
	Queue     reviewqueueport.ReviewQueueRepository
	// HardExamples is nil-safe: GatherAutoSignals and Run both treat a
	// nil HardExamples exactly like "mining has not been wired up for
	// this run" — see this plan's top-level deviation note and Task 7.
	HardExamples out.HardExampleRepository
	// LLM is nil-safe exactly like HardExamples -- see this plan's
	// top-level deviation note. Both must be non-nil for Run to invoke
	// mining; either nil skips it.
	LLM out.EditorialTeacherLLM
	// ArchivedLabels are read-only human-label repositories supplied from
	// archived SQLite snapshots by the CLI.
	ArchivedLabels []out.HumanLabelRepository
}

// Options configures one Run invocation.
type Options struct {
	DatasetPath        string
	MaxPerCluster      int
	CandidatesDir      string
	ModelPath          string
	TrustedSourceIDs   map[string]bool
	AutoLabelThreshold float64
	TrainCfg           editorialtrain.TrainConfig
	GateCfg            editorialtrain.GateConfig
	PromotionCfg       modelpromotion.PromotionCriteria
	Force              bool
	Auto               bool
	// TriggerReasons is set by the caller (cmd/train/run.go) when Auto
	// is true and retraintrigger.Evaluate found this invocation
	// triggered -- Run passes it straight into trainreport.Input,
	// unread otherwise.
	TriggerReasons []string
	// MiningBatchLimit, MiningMinNBConfidence configure the mining stage
	// (trainmine.Mine) Run invokes when Deps.LLM and Deps.HardExamples
	// are both set. Zero-valued by default -- MiningBatchLimit: 0 makes
	// trainmine.Mine itself a no-op (see its Options.BatchLimit <= 0
	// check), so this is safe even for callers that don't set it.
	MiningBatchLimit      int
	MiningMinNBConfidence float64
	// KnownTerms is the lowercase word set (built by cmd/train/run.go's
	// knownTermsFrom from the live dictionary + entity catalog) that
	// trainmine.UnknownTerms treats as "already known" when building the
	// report's catalog/dictionary visibility nudge.
	KnownTerms map[string]bool
}

// Outcome is Run's result: the persisted Run row (already Saved by the
// time Run returns) and its rendered report text (same string as
// Run.Report).
type Outcome struct {
	Run    training.Run
	Report string
	// Warnings are operator-facing notices the caller should print
	// alongside the report -- the same ones `train build` prints, e.g.
	// unresolved legacy label conflicts excluded from the dataset.
	Warnings []string
}

// Run executes build -> analyze -> evaluate -> promote in order. Any
// stage error aborts immediately: Run persists a Run row with
// Status="failed" and returns the error. A gate rejection at the
// promote stage is NOT an error -- Run returns (Outcome, nil) with
// Status="succeeded" and the report's Recommendation="REJECT".
//
// Safety guarantee, stated precisely: nothing that production reads is
// touched before the promote stage's gate check passes (or opts.Force
// is set). Concretely, until that point there is no promoted
// model_registry row (no row gains Status="promoted"/promoted_at) and
// the production model directory at opts.ModelPath is never written.
// The evaluate stage DOES write to the database: modeltraining.
// RunAndRegister registers every trained candidate with
// Status="candidate" before any verdict, so an evaluate- or
// promote-stage failure can leave candidate rows behind. That is
// expected and harmless -- nothing in production reads
// status='candidate' as authoritative; they are an audit trail of what
// was trained.
//
// One residual window is outside Run's control: modelpromotion.Promote
// copies the model artifact to opts.ModelPath before it opens its
// registry transaction, so a failure inside Promote's registry write
// can leave the new artifact on disk while the registry still names the
// old version. Recovery is `train rollback` (see docs/OPERATIONS.md).
func Run(ctx context.Context, deps Deps, opts Options, now time.Time) (Outcome, error) {
	runID := fmt.Sprintf("run-%d", now.UnixNano())
	startedAt := now

	fail := func(stage string, err error) (Outcome, error) {
		wrapped := fmt.Errorf("trainrun: %s: %w", stage, err)
		run := training.Run{
			ID: runID, StartedAt: startedAt, FinishedAt: time.Now().UTC(),
			Status: "failed", Report: wrapped.Error(),
		}
		if saveErr := deps.RunRepo.Save(ctx, run); saveErr != nil {
			return Outcome{}, fmt.Errorf("%w (additionally failed to save failed run: %v)", wrapped, saveErr)
		}
		return Outcome{}, wrapped
	}

	// --- build ---
	gold, err := editorialtrain.ExportDataset(ctx, deps.Labels, opts.TrustedSourceIDs)
	if err != nil {
		return fail("build", err)
	}
	goldGroups := [][]editorialtrain.TrainingExample{gold}
	for _, repo := range deps.ArchivedLabels {
		archived, exportErr := editorialtrain.ExportDataset(ctx, repo, opts.TrustedSourceIDs)
		if exportErr != nil {
			return fail("build archived labels", exportErr)
		}
		goldGroups = append(goldGroups, archived)
	}
	gold, err = editorialtrain.MergeTrainingExamples(goldGroups...)
	if err != nil {
		return fail("merge archived labels", err)
	}
	silver, silverSkippedMissingArticle, err := editorialtrain.FetchSilverExamples(ctx, deps.Proposals, deps.Labels, deps.Articles, opts.TrustedSourceIDs, opts.AutoLabelThreshold)
	if err != nil {
		return fail("build", err)
	}
	examples := editorialtrain.CapPerCluster(append(gold, silver...), opts.MaxPerCluster)
	if len(examples) == 0 {
		return fail("build", fmt.Errorf("no Gold or Silver examples found"))
	}
	if err := editorialtrain.SaveDataset(opts.DatasetPath, examples); err != nil {
		return fail("build", err)
	}
	dataset, err := editorialtrain.LoadDataset(opts.DatasetPath)
	if err != nil {
		return fail("build", err)
	}
	// The manifest must carry real provenance: `train build` and `train
	// run` default to the same dataset path, so hardcoded zeros here
	// would silently overwrite `train build`'s honest manifest and hide
	// the unresolved-conflict warning from an unattended pipeline.
	unresolvedConflicts, err := deps.Labels.CountUnresolvedConflicts(ctx)
	if err != nil {
		return fail("build", err)
	}
	var warnings []string
	if unresolvedConflicts > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"WARNING: %d article(s) have an unresolved legacy label conflict and are excluded from this dataset — see `label conflicts` and `label resolve-conflict`",
			unresolvedConflicts))
	}
	if err := editorialtrain.WriteManifest(opts.DatasetPath, examples, dataset.Hash(), now, unresolvedConflicts, silverSkippedMissingArticle); err != nil {
		return fail("build", err)
	}

	// --- analyze ---
	analysis, err := trainanalyze.Analyze(ctx, deps.Proposals, deps.Queue, opts.AutoLabelThreshold, queueLimit)
	if err != nil {
		return fail("analyze", err)
	}

	// --- mine ---
	var unknownTermsNote string
	var unresolvedHardExamples int
	if deps.LLM != nil && deps.HardExamples != nil {
		mineResult, err := trainmine.Mine(ctx, opts.ModelPath, deps.Articles, deps.LLM, deps.HardExamples,
			trainmine.Options{
				BatchLimit: opts.MiningBatchLimit, MinNBConfidence: opts.MiningMinNBConfidence,
				LLMAutoLabelThreshold: opts.AutoLabelThreshold,
			}, now)
		if err != nil {
			return fail("mine", err)
		}
		if !mineResult.Skipped {
			titles, err := titlesFor(ctx, deps.Articles, mineResult.HardPositives)
			if err != nil {
				return fail("mine", fmt.Errorf("resolve hard-positive article titles: %w", err))
			}
			unknownTermsNote = strings.Join(trainmine.UnknownTerms(titles, opts.KnownTerms), ", ")
		}
		// Surfaces mining's persisted state in the report: this is the
		// same count retraintrigger's MinHardExampleCount reads, so an
		// operator reading a run report can see what that trigger will
		// see next time.
		unresolvedHardExamples, err = deps.HardExamples.UnresolvedCount(ctx)
		if err != nil {
			return fail("mine", fmt.Errorf("read unresolved hard example count: %w", err))
		}
	}

	// --- evaluate ---
	promotedReport, err := modeltraining.EvaluatePromotedModel(opts.ModelPath, dataset, opts.TrainCfg)
	if err != nil {
		return fail("evaluate promoted baseline", err)
	}
	results, err := modeltraining.RunAndRegister(ctx, modeltraining.Deps{Registry: deps.Registry}, dataset, opts.TrainCfg, opts.CandidatesDir, opts.GateCfg, promotedReport)
	if err != nil {
		return fail("evaluate", err)
	}

	// --- promote (or reject) ---
	best, bestFound := bestPassingCandidate(results)

	var productionVersion string
	if promotedReport != nil {
		productionVersion = "(current promoted)"
	}

	goldCount, silverCount := len(gold), len(silver)
	var disagreementRate float64
	if len(examples) > 0 {
		disagreementRate = float64(analysis.QueueDisagreementCount) / float64(len(examples))
	}
	topSource, topSourceShare := editorialtrain.SourceConcentration(examples)
	reportIn := trainreport.Input{
		TopSource: topSource, TopSourceShare: topSourceShare,
		RunAt: now, Auto: opts.Auto, TriggerReasons: opts.TriggerReasons, UnknownTermsNote: unknownTermsNote,
		UnresolvedHardExampleCount: unresolvedHardExamples, Forced: opts.Force,
		TotalExamples: len(examples), GoldCount: goldCount, SilverCount: silverCount, DatasetHash: dataset.Hash(),
		ProposedCount: analysis.ProposedCount, AutoLabeledCount: analysis.AutoLabeledCount, SentToReviewCount: analysis.SentToReviewCount, ManualReviewRate: analysis.ManualReviewRate,
		QueueSize: analysis.QueueSize, QueueDisagreementCount: analysis.QueueDisagreementCount,
		QueueNovelVocabularyCount: analysis.QueueNovelVocabularyCount, QueueOtherCount: analysis.QueueOtherCount,
		// DisagreementCount/DisagreementRate reuse the review queue's
		// disagreement-component count as the NB/LLM disagreement signal
		// (reviewpriority's "disagreement" score component is precisely
		// NB-vs-LLM divergence — see domain/reviewpriority) --
		// there is no separate whole-dataset NB/LLM comparison pass in
		// this sub-project; that would be shadowcompare-shaped additional
		// scope, not needed to report this number meaningfully.
		DisagreementCount: analysis.QueueDisagreementCount,
		DisagreementRate:  disagreementRate,
		ProductionVersion: productionVersion,
		ProductionMetrics: promotedReport,
	}

	// persistedVersion is what reaches training_runs.model_version, which
	// has a FOREIGN KEY to model_registry.version: it stays empty (-> SQL
	// NULL) unless a real, registered candidate version exists. The
	// human-readable "(none)" placeholder belongs to the rendered report
	// only (reportIn.CandidateVersion), never to the persisted row.
	var persistedVersion string
	var resultSummary, recommendation string
	if !bestFound {
		recommendation = "REJECT"
		resultSummary = "rejected: no candidate cleared both gate sets"
		reportIn.CandidateVersion = "(none)"
		reportIn.CandidateMetrics = editorialtrain.Report{}
		reportIn.Gates = editorialtrain.GateResult{Passed: false, Failures: []string{"no candidate passed EditorialGates"}}
		reportIn.PromotionGates = editorialtrain.GateResult{Passed: false}
	} else {
		reportIn.CandidateVersion = best.Version
		persistedVersion = best.Version // registered by RunAndRegister, so the FK holds
		reportIn.CandidateMetrics = best.Candidate.TestReport
		gateResult, checkErr := modelpromotion.CheckGates(best.Dir, opts.GateCfg, opts.PromotionCfg, promotedReport)
		if checkErr != nil {
			return fail("promote", checkErr)
		}
		reportIn.Gates = best.Gates
		reportIn.PromotionGates = gateResultMinus(gateResult, best.Gates)

		if gateResult.Passed || opts.Force {
			if err := modelpromotion.Promote(ctx, modelpromotion.Deps{Registry: deps.Registry, UOW: deps.UOW}, best.Dir, best.Version, opts.ModelPath); err != nil {
				return fail("promote", err)
			}
			recommendation = "PROMOTE"
			resultSummary = fmt.Sprintf("promoted %s", best.Version)
		} else {
			recommendation = "REJECT"
			resultSummary = fmt.Sprintf("rejected: %s", joinFailures(gateResult.Failures))
		}
	}
	reportIn.Recommendation = recommendation
	reportIn.ResultSummary = resultSummary

	reportText := trainreport.Render(reportIn)
	run := training.Run{
		ID: runID, StartedAt: startedAt, FinishedAt: time.Now().UTC(),
		ModelVersion: persistedVersion, DatasetSummary: fmt.Sprintf(`{"gold":%d,"silver":%d,"hash":%q}`, goldCount, silverCount, dataset.Hash()),
		Status: "succeeded", Report: reportText,
	}
	if err := deps.RunRepo.Save(ctx, run); err != nil {
		return Outcome{}, fmt.Errorf("trainrun: save run record: %w", err)
	}
	return Outcome{Run: run, Report: reportText, Warnings: warnings}, nil
}

// bestPassingCandidate returns the Naive Bayes result when it clears
// EditorialGates, or (zero, false) otherwise. The offline LLM is a
// teacher only: the design requires Naive Bayes to remain the production
// classifier, so this defense-in-depth check must continue to reject
// every other algorithm even if a caller constructs TrainCfg directly.
//
// Selection policy, and a known divergence: this ranks by test F1,
// whereas modeltraining.SelectWinner -- the codebase's other
// auto-selection policy, used by editorialcycle -- ranks the very same
// filtered set by test PR-AUC. This sub-project's plan (task 9)
// specifies F1 here but records no rationale for differing from
// SelectWinner, so treat it as an open question flagged for a future
// unification rather than a settled design: F1 scores the operating
// point that will actually ship, PR-AUC scores threshold-independent
// discrimination; both are defensible, but two commands silently
// picking different winners from the same field is not. The
// trivial_majority exclusion above is likewise duplicated verbatim from
// SelectWinner and should become shared logic. Unification is
// deliberately out of scope here because it touches SelectWinner's
// existing editorialcycle call site.
func bestPassingCandidate(results []modeltraining.Result) (modeltraining.Result, bool) {
	var best modeltraining.Result
	found := false
	for _, r := range results {
		if r.Candidate.Model.Algorithm != "naive_bayes" {
			continue
		}
		if !r.Gates.Passed {
			continue
		}
		if !found || r.Candidate.TestReport.Overall.F1() > best.Candidate.TestReport.Overall.F1() {
			best, found = r, true
		}
	}
	return best, found
}

// gateResultMinus isolates the PromotionCriteria-only portion of a
// combined GateResult (as returned by modelpromotion.CheckGates) given
// the EditorialGates-only result computed separately by
// modeltraining.RunAndRegister -- the combined result's Failures slice
// is editorialGateResult.Failures followed by promotionFailures (see
// modelpromotion.CheckGates), so the promotion-only failures are
// whatever remains after the editorial ones.
func gateResultMinus(combined, editorialOnly editorialtrain.GateResult) editorialtrain.GateResult {
	if len(combined.Failures) < len(editorialOnly.Failures) {
		// Defensive: CheckGates re-evaluates the editorial gates from the
		// candidate's own metrics.json, so its editorial prefix should be
		// identical to editorialOnly. If it somehow is not, report the
		// combined failures rather than slicing out of range.
		return combined
	}
	promotionFailures := combined.Failures[len(editorialOnly.Failures):]
	return editorialtrain.GateResult{Passed: len(promotionFailures) == 0, Failures: promotionFailures}
}

func joinFailures(failures []string) string {
	if len(failures) == 0 {
		return "no failures recorded"
	}
	return strings.Join(failures, "; ")
}

// titlesFor resolves each candidate's article title via articles.FindByID,
// skipping (not failing on) a candidate whose article no longer exists --
// retention may have removed it since Mine scanned it, and the
// unknown-terms nudge is best-effort, not load-bearing.
func titlesFor(ctx context.Context, articles out.ArticleRepository, candidates []training.HardExampleCandidate) ([]string, error) {
	titles := make([]string, 0, len(candidates))
	for _, c := range candidates {
		a, err := articles.FindByID(ctx, c.ArticleID)
		if err != nil {
			if errors.Is(err, article.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("find article %s: %w", c.ArticleID, err)
		}
		titles = append(titles, a.Title)
	}
	return titles, nil
}
