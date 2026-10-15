// ports/out/source_policy_cooldown_repository.go
package out

import (
	"context"
	"time"
)

// SourcePolicyCooldownReservation is one attempt to claim
// (SourceID, ClusterID)'s cooldown window — see migration
// 0028_source_policy_cooldowns.sql and the design doc.
type SourcePolicyCooldownReservation struct {
	SourceID      string
	ClusterID     string
	ArticleID     string
	DecisionCode  string
	PolicyVersion string
}

// SourcePolicyCooldownRepository persists the source-policy cooldown
// reservation: the compare-and-swap that guarantees only an effective
// publication (or an actively-held reservation) consumes
// sourcepolicy.Policy.EventCooldown for a given (source_id, cluster_id)
// pair, independent of Gate B's own preliminary would_publish_special
// audit trail.
type SourcePolicyCooldownRepository interface {
	// Reserve attempts to atomically claim (r.SourceID, r.ClusterID)'s
	// cooldown window starting at now for duration cooldown. Succeeds
	// (acquired=true) when no row exists, the existing row is
	// 'released', or its cooldown_until has already elapsed; fails
	// (acquired=false, err=nil) when an active window is held by a
	// different reservation. Must run inside the same
	// ports/out.UnitOfWork.Do transaction as the rest of Gate B.
	Reserve(ctx context.Context, r SourcePolicyCooldownReservation, cooldown time.Duration, now time.Time) (acquired bool, err error)

	// ForceReserve unconditionally overwrites (r.SourceID, r.ClusterID)'s
	// window, bypassing the CAS condition entirely — used only for
	// MemberRoleCorrection, which bypasses the cooldown gate by design
	// but must still refresh the window so a later confirmation of the
	// same event respects a fresh cooldown.
	ForceReserve(ctx context.Context, r SourcePolicyCooldownReservation, cooldown time.Duration, now time.Time) error

	// ActiveCooldownUntil returns the cooldown_until of the current
	// 'reserved' or 'confirmed' row for (sourceID, clusterID) and true,
	// or the zero time and false if there is none (including if the only
	// row is 'released'). The caller computes
	// remaining = cooldownUntil.Sub(now); a non-positive result means no
	// cooldown is actually active.
	ActiveCooldownUntil(ctx context.Context, sourceID, clusterID string) (time.Time, bool, error)

	// Confirm marks the reservation confirmed when the outbox item it
	// was reserved for is actually delivered. Scoped to (sourceID,
	// clusterID, articleID): affected=false, err=nil if articleID does
	// not currently hold the reservation (e.g. a later candidate already
	// reserved over it).
	Confirm(ctx context.Context, sourceID, clusterID, articleID string, outboxID int64, now time.Time) (affected bool, err error)

	// Release marks the reservation released on a terminal
	// without-publishing outcome (editorial/freshness/cluster-gate
	// rejection, or a terminal outbox failure). Scoped exactly like
	// Confirm.
	Release(ctx context.Context, sourceID, clusterID, articleID string, now time.Time) (affected bool, err error)
}
