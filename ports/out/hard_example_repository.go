package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// HardExampleRepository persists hard-negative/hard-positive mining
// candidates — see
// domain/training.HardExampleCandidate.
type HardExampleRepository interface {
	// Upsert inserts c, or refreshes its scores/mined_at in place if
	// c.ArticleID already exists — re-mining the same article never
	// duplicates it.
	Upsert(ctx context.Context, c training.HardExampleCandidate) error
	// UnresolvedCount returns how many candidates have no resolved_at —
	// the source for application/retraintrigger.Signals.HardExampleCount.
	UnresolvedCount(ctx context.Context) (int, error)
	// ListUnresolved returns up to limit unresolved candidates, most
	// recently mined first.
	ListUnresolved(ctx context.Context, limit int) ([]training.HardExampleCandidate, error)
	// IsUnresolved reports whether articleID has a candidate row with no
	// resolved_at — the per-article existence check
	// application/editorialreview.Sync needs to set
	// reviewpriority.Signals.HardMiningCount. False (not an error) for an
	// article with no candidate row at all, or an already-resolved one.
	IsUnresolved(ctx context.Context, articleID string) (bool, error)
	// MarkResolved sets articleID's resolved_at, if it has an
	// unresolved candidate row; a no-op (not an error) if articleID has
	// no candidate row at all or is already resolved.
	MarkResolved(ctx context.Context, articleID string, resolvedAt time.Time) error
}
