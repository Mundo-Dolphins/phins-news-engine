package out

import "context"

// Checkpointer runs a SQLite WAL checkpoint. RunRetention calls it last, in
// PASSIVE (non-blocking) mode, to bound WAL file growth without stalling
// writers.
type Checkpointer interface {
	Checkpoint(ctx context.Context) error
}
