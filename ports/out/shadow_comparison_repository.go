package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// ShadowComparisonRepository persists offline shadow-comparison
// disagreements — audit/analysis data only, never
// read by the runtime pipeline.
type ShadowComparisonRepository interface {
	// SaveDisagreements inserts rows, replacing any existing row sharing
	// the same (RunID, ArticleID) — safe to call more than once for the
	// same run without producing duplicates.
	SaveDisagreements(ctx context.Context, rows []training.ShadowDisagreement) error
	// ListByRun returns every disagreement recorded under runID, most
	// recently created first.
	ListByRun(ctx context.Context, runID string) ([]training.ShadowDisagreement, error)
}
