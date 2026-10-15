package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// TrainingRunRepository persists one row per `train run` invocation
// — audit/analysis data plus the input sub-project B's
// retrain-trigger checker needs to compute deltas since the last run.
type TrainingRunRepository interface {
	// Save inserts r, or updates it in place if its ID already exists.
	Save(ctx context.Context, r training.Run) error
	// Latest returns the most recently started run, or nil if none
	// exists yet -- regardless of its Status, so a failed or still-
	// "running" row can come back. Most callers that need a stable
	// dataset-summary baseline (e.g. trainrun.GatherAutoSignals) want
	// LatestSucceeded instead.
	Latest(ctx context.Context) (*training.Run, error)
	// LatestSucceeded returns the most recently started run with
	// Status="succeeded", or nil if none exists yet. Unlike Latest, a
	// failed run never shadows an earlier success here -- this exists
	// so a baseline read (trainrun.GatherAutoSignals' Gold/Silver diff)
	// does not silently reset to zero after a training failure, which
	// would otherwise make the "new examples" trigger fire every night
	// at full LLM cost until a run finally succeeds again.
	LatestSucceeded(ctx context.Context) (*training.Run, error)
}
