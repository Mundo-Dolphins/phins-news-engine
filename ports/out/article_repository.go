// Package out defines the driven ports the application depends on:
// persistence, fetching, publishing, clock and logging. Adapters implement
// these interfaces; the application never depends on a concrete adapter.
package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// ArticleRepository persists articles and answers the queries the pipeline
// needs.
type ArticleRepository interface {
	// Save inserts a, or updates it in place if its ID already exists.
	Save(ctx context.Context, a article.Article) error
	// FindByID returns the article with the given ID, or a wrapped
	// sql.ErrNoRows if none exists.
	FindByID(ctx context.Context, id string) (article.Article, error)
	// DeleteByStatusOlderThan removes articles whose status is one of
	// statuses and whose fetched_at is before cutoff, returning how many
	// rows were deleted. Never pass article.StatusPublished: published
	// articles are never deleted by retention.
	DeleteByStatusOlderThan(ctx context.Context, statuses []article.Status, cutoff time.Time) (int, error)
	// Count returns the total number of stored articles.
	Count(ctx context.Context) (int, error)
	// ListCandidatesForReview returns up to limit articles fetched at or
	// after since, in a status editorial review is meaningful for
	// (clustered, pending_publication, published, rejected_quality), that
	// have no row in human_labels yet, oldest-fetched first. Used to build
	// the active-learning candidate pool (application/activelearning) —
	// bounded by limit, never a full-table scan.
	ListCandidatesForReview(ctx context.Context, since time.Time, limit int) ([]article.Article, error)
	// ListByClusterID returns up to limit other articles sharing
	// clusterID, excluding excludeArticleID, most-recently-fetched first.
	// Used by the review screen's "similar articles" display.
	ListByClusterID(ctx context.Context, clusterID, excludeArticleID string, limit int) ([]article.Article, error)
	// FindByCanonicalURL returns the article whose CanonicalURL matches
	// url (oldest published_at first, if more than one somehow shares a
	// URL), or an error satisfying errors.Is(err, article.ErrNotFound)
	// (which also satisfies errors.Is(err, sql.ErrNoRows) against the
	// sqlite adapter) if none exists. Any other error — a locked/corrupt
	// database, I/O failure, canceled context, ... — is returned as-is
	// and must NOT be treated as "not found". Used by
	// application/telegramimport to match an imported channel message
	// back to a known local article.
	FindByCanonicalURL(ctx context.Context, url string) (article.Article, error)
}
