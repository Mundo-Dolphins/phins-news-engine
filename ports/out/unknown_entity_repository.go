package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// UnknownEntityRepository persists raw evidence of name-like spans the
// deterministic extractor (domain/eventsignals) could not match
// to any catalog entity — append-only, one row per
// occurrence. Kept separate from ClusterRepository: this is a different
// concern (evidence for future catalog maintenance), not part of the
// clustering join decision ClusterRepository's methods all serve.
type UnknownEntityRepository interface {
	// SaveOccurrences persists occurrences (rep.UnknownEntities from one
	// article's eventsignals.Extract call) against articleID. Must be
	// called after the article row already exists (articleID references
	// articles(id)). A no-op (returns nil without touching the database)
	// when occurrences is empty.
	SaveOccurrences(ctx context.Context, articleID string, occurrences []eventsignals.UnknownEntity, rulesVersion string, now time.Time) error
}
