package trainrun

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/app/retraintrigger"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainanalyze"
)

// datasetSummary mirrors the JSON shape Run's success path writes to
// training.Run.DatasetSummary (see trainrun.go's fmt.Sprintf call) --
// GatherAutoSignals reads it back as the "since the last run" Gold/
// Silver baseline.
type datasetSummary struct {
	Gold   int    `json:"gold"`
	Silver int    `json:"silver"`
	Hash   string `json:"hash"`
}

// GatherAutoSignals builds retraintrigger.Signals from cheap, LLM-free
// reads: the previous SUCCEEDED training_runs row
// (via deps.RunRepo.LatestSucceeded) as the Gold/Silver baseline,
// freshly re-counted current Gold/Silver examples (the same reads
// `train build`/`train run`'s build stage performs --
// ExportDataset/FetchSilverExamples read stored labels/proposals, never
// the LLM), the review queue's disagreement/novel-vocabulary breakdown
// (trainanalyze.Analyze, also LLM-free), the currently-promoted
// model's own ManualReviewRate (0 if there is no promoted model yet),
// and the HardExampleCount from deps.HardExamples.UnresolvedCount when
// deps.HardExamples is non-nil, defaulting to 0 when nil.
//
// Known limitation -- ManualErrorRate is a latching, non-trending
// signal: it is read once per promoted model, not compared against a
// prior run's value, so once it exceeds max_manual_error_rate it fires
// every night until either a different model gets promoted or the
// operator disables/raises the threshold. See
// config.RetrainTriggersConfig.MaxManualErrorRate's doc comment for the
// operator-facing version of this warning.
func GatherAutoSignals(ctx context.Context, deps Deps, opts Options) (retraintrigger.Signals, error) {
	// LatestSucceeded (not Latest) is deliberate: a failed run's
	// DatasetSummary is always empty (see trainrun.Run's fail() path),
	// so keying the baseline off the single most recent row regardless
	// of status would silently reset the baseline to zero after every
	// failure -- turning one Ollama outage / bad split / disk error
	// into a "new examples" trigger that fires at full LLM cost every
	// subsequent night until a run finally succeeds. Using the latest
	// SUCCESS instead means the very next successful run's "new
	// examples" count reflects everything accumulated since the last
	// success, not since the last attempt -- arguably the intended
	// behavior, and it eliminates the repeated-triggering failure mode.
	latest, err := deps.RunRepo.LatestSucceeded(ctx)
	if err != nil {
		return retraintrigger.Signals{}, fmt.Errorf("trainrun: gather auto signals: read latest succeeded run: %w", err)
	}
	var baseline datasetSummary
	if latest != nil && latest.DatasetSummary != "" {
		if err := json.Unmarshal([]byte(latest.DatasetSummary), &baseline); err != nil {
			return retraintrigger.Signals{}, fmt.Errorf("trainrun: gather auto signals: parse latest run's dataset summary %q: %w", latest.DatasetSummary, err)
		}
	}

	gold, err := editorialtrain.ExportDataset(ctx, deps.Labels, opts.TrustedSourceIDs)
	if err != nil {
		return retraintrigger.Signals{}, fmt.Errorf("trainrun: gather auto signals: count gold examples: %w", err)
	}
	silver, _, err := editorialtrain.FetchSilverExamples(ctx, deps.Proposals, deps.Labels, deps.Articles, opts.TrustedSourceIDs, opts.AutoLabelThreshold)
	if err != nil {
		return retraintrigger.Signals{}, fmt.Errorf("trainrun: gather auto signals: count silver examples: %w", err)
	}

	analysis, err := trainanalyze.Analyze(ctx, deps.Proposals, deps.Queue, opts.AutoLabelThreshold, queueLimit)
	if err != nil {
		return retraintrigger.Signals{}, fmt.Errorf("trainrun: gather auto signals: analyze: %w", err)
	}

	// Denominator must match trainrun.go's Run: it computes the same-
	// named report metric as QueueDisagreementCount / len(examples)
	// where examples is gold+silver AFTER editorialtrain.CapPerCluster
	// -- a smaller number whenever opts.MaxPerCluster limits examples
	// per cluster. Using the raw pre-cap gold+silver count here would
	// silently disagree with the number operators see (and calibrate
	// max_disagreement_rate against) in the training report.
	examples := editorialtrain.CapPerCluster(append(append([]editorialtrain.TrainingExample{}, gold...), silver...), opts.MaxPerCluster)
	var disagreementRate float64
	if len(examples) > 0 {
		disagreementRate = float64(analysis.QueueDisagreementCount) / float64(len(examples))
	}

	var manualErrorRate float64
	if promoted := modeltraining.LoadPromotedReport(opts.ModelPath); promoted != nil {
		manualErrorRate = promoted.ManualReviewRate
	}

	hardExampleCount := 0
	if deps.HardExamples != nil {
		hardExampleCount, err = deps.HardExamples.UnresolvedCount(ctx)
		if err != nil {
			return retraintrigger.Signals{}, fmt.Errorf("trainrun: gather auto signals: count unresolved hard examples: %w", err)
		}
	}

	return retraintrigger.Signals{
		NewGoldSinceLastRun:   nonNegative(len(gold) - baseline.Gold),
		NewSilverSinceLastRun: nonNegative(len(silver) - baseline.Silver),
		DisagreementRate:      disagreementRate,
		ManualErrorRate:       manualErrorRate,
		NovelVocabularyCount:  analysis.QueueNovelVocabularyCount,
		HardExampleCount:      hardExampleCount,
	}, nil
}

// nonNegative clamps a count-difference to 0 -- a shrinking Gold/Silver
// set (e.g. a relabel that moved examples out of scope) must never
// read as a negative "new examples" signal.
func nonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
