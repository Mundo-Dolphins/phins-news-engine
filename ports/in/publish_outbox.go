package in

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

// PublishOutboxResult summarizes the outcome of one outbox-drain pass.
type PublishOutboxResult struct {
	Claimed   int
	Published int
	Retried   int
	Discarded int
	// Expired counts articles that became stale while waiting in the outbox.
	Expired            int
	Deferred           int
	AmbiguousDetected  int
	ReconciledPositive int
	ReconciledNegative int
	Quarantined        int
	// CooldownConfirmed counts outbox deliveries where a held
	// source-policy cooldown reservation was actually confirmed
	// (SourcePolicyCooldownRepository.Confirm returned affected=true).
	CooldownConfirmed int
	// CooldownReleased counts terminal outbox failures where a held
	// source-policy cooldown reservation was actually released
	// (SourcePolicyCooldownRepository.Release returned affected=true).
	CooldownReleased int
	// ByDestination breaks the counters above down per outbox destination.
	// Only destinations that claimed or processed a row appear.
	ByDestination map[publishing.Destination]*DestinationCounts
}

// DestinationCounts is the per-destination slice of PublishOutboxResult.
type DestinationCounts struct {
	Claimed           int
	Published         int
	Retried           int
	Discarded         int
	Expired           int
	Deferred          int
	AmbiguousDetected int
}

// ForDestination returns the (created on demand) counters for destination;
// an empty destination means Telegram.
func (r *PublishOutboxResult) ForDestination(destination publishing.Destination) *DestinationCounts {
	destination = destination.Normalized()
	if r.ByDestination == nil {
		r.ByDestination = make(map[publishing.Destination]*DestinationCounts)
	}
	c, ok := r.ByDestination[destination]
	if !ok {
		c = &DestinationCounts{}
		r.ByDestination[destination] = c
	}
	return c
}

// ReconcileResult summarizes one standalone Reconcile pass — the same
// counters PublishOutboxResult folds in when Run calls Reconcile first,
// exposed separately so the CLI's `telegram outbox reconcile` command can
// invoke and report on it without running a full claim/publish cycle.
type ReconcileResult struct {
	Checked            int
	ReconciledPositive int
	ReconciledNegative int
	Quarantined        int
}

// PublishOutbox claims due outbox items and attempts to publish them,
// applying retry/backoff on temporary failures and discarding permanently
// failed or retry-exhausted ones. See ADR 0007.
type PublishOutbox interface {
	Run(ctx context.Context) (PublishOutboxResult, error)
}
