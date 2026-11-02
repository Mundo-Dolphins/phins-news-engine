package out

import (
	"context"
	"time"
)

// MediaSweepResult summarises one MediaSweeper.DeleteOlderThan run.
type MediaSweepResult struct {
	// Deleted is the number of files removed and BytesDeleted their size.
	Deleted      int
	BytesDeleted int64
	// Protected is the number of files past the cutoff that were kept because
	// they are still referenced.
	Protected int
}

// MediaSweeper removes expired files from the media store (ADR 0036).
// Fetching goes through EventMediaStore; the two are implemented by the same
// adapter.
type MediaSweeper interface {
	// DeleteOlderThan removes every stored file last written or re-fetched
	// before cutoff, except the paths in keep (relative media-store paths, as
	// in publishing.ImageRef.Path). A file that has already vanished is not an
	// error.
	DeleteOlderThan(ctx context.Context, cutoff time.Time, keep map[string]struct{}) (MediaSweepResult, error)
}
