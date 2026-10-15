package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

// OutboxExpirationFilter selects old, claimable publications. PublishedBefore
// is compared to the source article's published_at, never to queue timestamps.
type OutboxExpirationFilter struct {
	// PublishedBefore keeps the operator's legacy age filter. It is also the
	// deterministic fallback for rows with NULL expires_at.
	PublishedBefore time.Time
	// ExpiredBefore is the immutable deadline cutoff for known rows.
	ExpiredBefore time.Time
	ContentType   article.ContentType
	SourceID      string
	Limit         int
}

// OutboxExpirationCandidate is the operator-safe projection shown before an
// expiration is applied. It intentionally excludes payload/article body data.
type OutboxExpirationCandidate struct {
	OutboxID    int64
	ArticleID   string
	Status      publishing.OutboxStatus
	PublishedAt time.Time
	ExpiresAt   time.Time
	ContentType article.ContentType
	SourceID    string
	Title       string
}

// OutboxExpirationRepository is the persistence boundary for bulk backlog
// expiration. Implementations must only select and transition claimable
// pending/retryable rows.
type OutboxExpirationRepository interface {
	ListExpirationCandidates(ctx context.Context, filter OutboxExpirationFilter) ([]OutboxExpirationCandidate, error)
	ExpireClaimable(ctx context.Context, outboxID int64, articleID, reason string, now time.Time) error
}
