package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// HumanLabelRepository persists human-provided training labels as
// append-only history: every write adds a new revision and, if it
// supersedes a prior one, flips that prior row's active flag off --
// nothing is ever deleted or updated in place. At most one revision per
// article_id is active at a time (enforced by a real SQLite constraint,
// not application discipline alone -- see migration 0026).
//
// human_labels rows are an operational journal (used for review-session
// bookkeeping and ListAll's active-learning exclusion set) and are
// subject to human_labels.article_id's ON DELETE CASCADE when the
// underlying article is eventually purged by retention. That's fine:
// every Label/Relabel durably mirrors the revision, at the moment it's
// written, into editorial_examples -- a table with no foreign key to
// articles -- so training data survives regardless of what later
// happens to the operational article or even to this row itself.
type HumanLabelRepository interface {
	// Label creates the first active revision for e.ArticleID, writing
	// to human_labels and editorial_examples atomically. Returns
	// training.ErrAlreadyLabeled (wrapped) if an active revision already
	// exists -- callers must use Relabel. Returns an error if
	// e.ArticleID does not name an existing article.
	Label(ctx context.Context, e training.Example) error

	// Relabel supersedes the current active revision -- which must be
	// at expectedRevision (read via GetActive) -- with a new one built
	// from e, atomically. Returns training.ErrNotLabeled (wrapped) if
	// there is no active revision, or training.ErrRevisionConflict
	// (wrapped) if the active revision is not expectedRevision (another
	// writer already relabeled concurrently).
	Relabel(ctx context.Context, e training.Example, expectedRevision int) error

	// Retract supersedes the current active revision -- which must be
	// at expectedRevision -- with nothing: articleID becomes unlabeled
	// again, its history preserved. Same error cases as Relabel.
	Retract(ctx context.Context, articleID string, expectedRevision int) error

	// GetActive returns the current active label for articleID, if any.
	GetActive(ctx context.Context, articleID string) (training.Example, bool, error)

	// ListByArticle returns every revision for articleID, oldest first
	// -- active and superseded alike.
	ListByArticle(ctx context.Context, articleID string) ([]training.Example, error)

	// ListAll returns the currently active label for every article --
	// used by application/activelearning to exclude already-decided
	// articles from the review candidate pool. Not used by dataset
	// export -- see ListEditorialExamples.
	ListAll(ctx context.Context) ([]training.Example, error)

	// ListEditorialExamples returns the active durable snapshot for
	// every article, oldest labeled_at first -- the sole source
	// editorialtrain.ExportDataset reads from. At most one row per
	// article_id, by construction (the same active-flag invariant
	// Label/Relabel/Retract maintain).
	ListEditorialExamples(ctx context.Context) ([]training.EditorialExample, error)

	// CountEditorialExamples returns how many durable snapshot revisions
	// exist in total -- active and superseded alike. Used by
	// RunRetention to report a per-run "examples retained" metric: this
	// count is strictly non-decreasing now that relabeling appends a
	// row instead of deleting one.
	CountEditorialExamples(ctx context.Context) (int, error)

	// ListUnresolvedConflicts returns every human_label_conflicts row
	// (from either table) with no resolved_at, most recently detected
	// first.
	ListUnresolvedConflicts(ctx context.Context) ([]training.Conflict, error)

	// CountUnresolvedConflicts returns how many distinct article_ids
	// have at least one unresolved conflict -- used by `train export` to
	// print a warning banner (same pattern as
	// editorialtrain.Dataset.UnparseableSourceIDCount).
	CountUnresolvedConflicts(ctx context.Context) (int, error)

	// ResolveConflict writes e as articleID's new active revision
	// (exactly like Label -- articleID has zero active revisions while
	// its conflict is unresolved) and marks every human_label_conflicts
	// row for articleID (in both tables) resolved, atomically. Returns
	// an error if articleID has no unresolved conflict.
	//
	// If articleID's article no longer exists (a conflict detected on
	// editorial_examples alone can outlive a retention-purged article),
	// the conflict is still marked resolved but no active revision is
	// created: there is no content left to export, so resolving means
	// acknowledging the conflict, not producing a training example.
	ResolveConflict(ctx context.Context, articleID string, e training.Example) error
}
