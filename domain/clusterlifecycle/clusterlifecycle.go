// Package clusterlifecycle decides when an open event_clusters row is
// inactive enough to close. It is pure (no I/O, no wall-clock access) —
// it exists alongside the SQL
// in ClusterRepository.CloseInactiveBatch, which re-implements the same
// comparison directly so closing stays a single atomic statement.
package clusterlifecycle

import "time"

// Eligible reports whether an open cluster last active at lastMemberAt is
// eligible to close at now, given closeAfter. The boundary is inclusive:
// a cluster exactly closeAfter old is eligible. Only meaningful for a
// cluster currently in clustering.StatusOpen — callers own the status
// check, mirroring clustergate.Decide's "caller supplies exactly the
// facts it hasn't already computed" shape.
func Eligible(lastMemberAt, now time.Time, closeAfter time.Duration) bool {
	return now.Sub(lastMemberAt) >= closeAfter
}
