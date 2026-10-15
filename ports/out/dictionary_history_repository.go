package out

import (
	"context"
	"time"
)

// DictionaryHistoryRecord is one entry in the append-only dictionary
// history ledger: a single update or rollback operation that produced (or
// re-promoted) a dictionary version.
type DictionaryHistoryRecord struct {
	Version            string
	ContentHash        string
	CreatedAt          time.Time
	ChangeCount        int
	SourceSnapshotRef  string
	OperationKind      string
	PredecessorVersion string
	Promoted           bool
}

// DictionaryHistoryRepository persists the append-only dictionary history
// ledger. Every update/rollback operation adds a row; rows are never
// deleted. This sits alongside (not replacing) DictionaryRegistryRepository,
// which tracks only the single currently-promoted version.
type DictionaryHistoryRepository interface {
	// Record inserts r, or updates it in place if its Version already
	// exists.
	Record(ctx context.Context, r DictionaryHistoryRecord) error
	// List returns every history record, newest first.
	List(ctx context.Context) ([]DictionaryHistoryRecord, error)
	// Get returns the history record with the given version, or a wrapped
	// sql.ErrNoRows if none exists.
	Get(ctx context.Context, version string) (DictionaryHistoryRecord, error)
	// SetPromoted marks the given version as promoted.
	SetPromoted(ctx context.Context, version string) error
}
