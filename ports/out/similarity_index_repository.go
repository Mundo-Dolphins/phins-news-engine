package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// CandidateFilter narrows near-duplicate/same-event candidate generation
// to a SQL-filterable subset before any similarity score is computed —
// this is what avoids comparing every incoming article against the entire
// historical article set.
type CandidateFilter struct {
	WindowStart     time.Time
	ContentType     article.ContentType
	ExcludeStatuses []article.Status
	Terms           []string
	MinSharedTerms  int
	Limit           int
}

// Candidate is one article returned by FindCandidates as a plausible
// near-duplicate or same-event match.
type Candidate struct {
	ArticleID string
	// ClusterID is never empty for a candidate returned by this query:
	// every article that reaches candidate generation has already been
	// assigned a cluster (see ADR 0009's invariant).
	ClusterID   string
	Title       string
	ContentType article.ContentType
	SourceID    string
	PublishedAt time.Time
	Status      article.Status
}

// SimilarityIndexRepository maintains an inverted index of dedup shingles
// per article and answers SQL-filtered candidate queries against it.
type SimilarityIndexRepository interface {
	// IndexTerms records terms (the article's dedup shingles) against
	// articleID. Safe to call more than once; duplicate (articleID, term)
	// pairs are ignored.
	IndexTerms(ctx context.Context, articleID string, terms []string) error
	// FindCandidates returns articles sharing at least filter.MinSharedTerms
	// of filter.Terms, within filter.WindowStart, matching
	// filter.ContentType, excluding filter.ExcludeStatuses, ordered
	// most-recent-first and capped at filter.Limit.
	FindCandidates(ctx context.Context, filter CandidateFilter) ([]Candidate, error)
}
