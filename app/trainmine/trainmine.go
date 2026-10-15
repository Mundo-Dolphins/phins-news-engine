package trainmine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialteacher"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Options configures one Mine scan.
type Options struct {
	// BatchLimit bounds how many candidate articles this scan
	// considers in total (offline_training.mining.batch_limit).
	BatchLimit int
	// MinNBConfidence is the NB-side confidence floor
	// (offline_training.mining.min_nb_confidence).
	MinNBConfidence float64
	// LLMAutoLabelThreshold is the LLM-side confidence floor
	// (offline_training.llm.auto_label_threshold, reused as-is).
	LLMAutoLabelThreshold float64
	// BatchSize is how many articles are sent to the LLM per
	// llm.Classify call; 0 defaults to 20, matching editorialteacher.Classify.
	BatchSize int
}

// Result summarizes one Mine call.
type Result struct {
	ArticlesScored    int
	HardNegativeCount int
	HardPositiveCount int
	// HardPositives holds every candidate upserted this call with
	// Kind == "positive" (in upsert order) — trainrun.Run's mining step
	// (Task 7) reads this to build the report's unknown-vocabulary nudge,
	// since a hard-positive article's title is exactly what Part 26 wants
	// surfaced ("new players, new coaches, new roster terminology").
	HardPositives []training.HardExampleCandidate
	// Skipped is true only when there is no promoted model yet — not an
	// error, since there is genuinely nothing to compare LLM labels
	// against on the very first run.
	Skipped    bool
	SkipReason string
}

// nbConfidence maps a Score onto [0,1], confident toward whichever zone
// Score.Zone actually landed in — 1.0 at Score.Value's extreme for that
// zone (100 for accept, 0 for reject), decaying toward the *model's own
// threshold* at the zone boundary, not toward a fixed 0.5: an accept
// score exactly at the boundary returns Thresholds.Accept/100, and a
// reject score exactly at the boundary returns
// (100-Thresholds.Reject)/100. ZoneAmbiguous always returns 0, since an
// ambiguous prediction is never "confident" in either direction.
//
// Consequence worth knowing when tuning: because the floor of each
// zone's confidence range is the promoted model's threshold, the
// practical selectivity of offline_training.mining.min_nb_confidence is
// coupled to wherever that model's thresholds happen to sit. With
// Thresholds.Accept = 80, every accept-zone prediction already scores
// >= 0.80, so a min_nb_confidence of 0.7 filters nothing on that side;
// with Thresholds.Accept = 95 it filters nothing below 0.95. Re-check
// this setting whenever SelectThresholds moves a promoted model's
// boundaries.
func nbConfidence(s editorial.Score) float64 {
	switch s.Zone {
	case editorial.ZoneAccept:
		return s.Value / 100
	case editorial.ZoneReject:
		return (100 - s.Value) / 100
	default:
		return 0
	}
}

// Mine scans up to opts.BatchLimit articles from articles.ListCandidatesForReview
// (no active human_label — the same "unlabeled" pool `train build`'s
// Silver assembly draws from), scores each with the promoted NB model
// at modelPath, classifies each with llm (the same appout.EditorialTeacherLLM
// port editorial classify uses, called directly here — Mine never saves
// an editoriallabelproposal.Proposal or marks articles processed; it
// only compares), and upserts every strong NB/LLM disagreement into repo.
// Returns Result{Skipped: true} (not an error) if modelPath has no
// promoted model yet. A batch-level LLM failure aborts the whole scan
// with a non-nil error; articles already scored via earlier batches
// keep their upserted rows (each batch is one all-or-nothing unit).
func Mine(ctx context.Context, modelPath string, articles appout.ArticleRepository, llm appout.EditorialTeacherLLM, repo appout.HardExampleRepository, opts Options, now time.Time) (Result, error) {
	model, err := editorialmodel.Load(modelPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{Skipped: true, SkipReason: "no promoted model, skipping mining"}, nil
		}
		return Result{}, fmt.Errorf("trainmine: load promoted model: %w", err)
	}

	limit := opts.BatchLimit
	if limit <= 0 {
		return Result{}, nil
	}
	candidates, err := articles.ListCandidatesForReview(ctx, time.Time{}, limit)
	if err != nil {
		return Result{}, fmt.Errorf("trainmine: list candidate articles: %w", err)
	}

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 20
	}

	result := Result{}
	for start := 0; start < len(candidates); start += batchSize {
		end := start + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[start:end]

		teacherArticles := make([]appout.EditorialTeacherArticle, len(batch))
		byID := make(map[string]int, len(batch))
		batchIDs := make(map[string]bool, len(batch))
		for i, a := range batch {
			teacherArticles[i] = editorialteacher.BuildTeacherArticle(a)
			byID[a.ID] = i
			batchIDs[a.ID] = true
		}

		res, err := llm.Classify(ctx, editorialteacher.PublicationPolicy, teacherArticles)
		if err != nil {
			return result, fmt.Errorf("trainmine: classify batch: %w", err)
		}

		for _, l := range res.Labels {
			if err := editorialteacher.ValidateLabel(l, batchIDs); err != nil {
				continue
			}
			a := batch[byID[l.ArticleID]]
			fv := editorial.ExtractFeatures(a, false)
			score := editorial.Predict(model, fv)
			result.ArticlesScored++

			kind := ClassifyHardExample(score.Zone, nbConfidence(score), l.Label, l.Confidence, opts.MinNBConfidence, opts.LLMAutoLabelThreshold)
			if kind == "" {
				continue
			}
			candidate := training.HardExampleCandidate{
				ArticleID: a.ID, Kind: kind, NBScore: score.Value,
				LLMLabel: l.Label, LLMConfidence: l.Confidence, MinedAt: now,
			}
			if err := repo.Upsert(ctx, candidate); err != nil {
				return result, fmt.Errorf("trainmine: upsert hard example candidate %s: %w", a.ID, err)
			}
			if kind == "negative" {
				result.HardNegativeCount++
			} else {
				result.HardPositiveCount++
				result.HardPositives = append(result.HardPositives, candidate)
			}
		}
	}
	return result, nil
}
