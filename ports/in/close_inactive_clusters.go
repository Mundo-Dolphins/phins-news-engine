package in

import (
	"context"
	"time"
)

// CloseInactiveClustersConfig configures the cluster-closing maintenance
// job.
type CloseInactiveClustersConfig struct {
	// CloseAfter is how long an open cluster may go without a new member
	// before it becomes eligible to close (clusterlifecycle.Eligible's
	// closeAfter). Recommended default: 96h — see the design doc's
	// "Timing invariant" for why this must satisfy
	// CloseAfter + (retention window for closed clusters) >= the widest
	// configured clustering candidate window.
	CloseAfter time.Duration
	// BatchSize bounds one CloseInactiveBatch call's blast radius.
	BatchSize int
	// MaxBatchesPerRun bounds how many CloseInactiveBatch calls one Run
	// makes, independent of TimeBudget.
	MaxBatchesPerRun int
	// TimeBudget bounds how long one Run keeps looping, checked via the
	// injected Clock before each batch (not real wall-clock sleep), so
	// it's exercisable with a fake clock in tests.
	TimeBudget time.Duration
	// CloseReason is stored verbatim on every closed cluster's
	// close_reason column.
	CloseReason string
}

// CloseInactiveClustersResult summarizes one Run call.
type CloseInactiveClustersResult struct {
	ClustersClosed int
	BatchesRun     int
}

// CloseInactiveClusters closes open event_clusters rows that have had no
// new member for at least CloseInactiveClustersConfig.CloseAfter, in
// batches bounded by both count and time, idempotently and resumably: an
// interrupted run leaves no partial state, and a subsequent Run call picks
// up exactly where it left off because CloseInactiveBatch's own
// compare-and-swap WHERE clause is the only "cursor" this job needs.
type CloseInactiveClusters interface {
	Run(ctx context.Context) (CloseInactiveClustersResult, error)
}
