package in

import (
	"context"
	"time"
)

// RetentionWindows configures how far back each retention category looks
// before deleting data. Published articles, human labels, and promoted
// model/dictionary registry versions are never deleted regardless of these
// windows — see ADR 0005.
type RetentionWindows struct {
	RejectedArticles    time.Duration
	Duplicates          time.Duration
	AuditEvents         time.Duration
	PublicationAttempts time.Duration
	Clusters            time.Duration
}

// RetentionResult summarizes how many rows each category deleted.
type RetentionResult struct {
	RejectedArticlesDeleted int
	DuplicatesDeleted       int
	AuditEventsDeleted      int
	AttemptsDeleted         int
	ClustersDeleted         int
	// EditorialExamplesRetained is the total number of durable editorial
	// example revisions (training.EditorialExample rows) retained after
	// this run — active and superseded alike. It is strictly
	// non-decreasing now that relabeling appends a revision instead of
	// deleting one (#165), so it remains a per-run signal that the
	// training corpus is monotonically non-decreasing, i.e. proof
	// retention did not silently lose any of it.
	EditorialExamplesRetained int
}

// RunRetention deletes data older than its configured retention window, per
// category, honoring the never-delete exceptions.
type RunRetention interface {
	Run(ctx context.Context) (RetentionResult, error)
}
