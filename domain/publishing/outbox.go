// Package publishing (this file) models the transactional-outbox side of
// publication: queue status, attempt outcomes, and retry backoff. See ADR
// 0007 for the outbox design and ADR 0030 for the sent_unconfirmed/quarantined repair flow.
package publishing

import "time"

// OutboxStatus is the lifecycle of one queued publication.
type OutboxStatus string

const (
	OutboxPending         OutboxStatus = "pending"
	OutboxLeased          OutboxStatus = "leased"
	OutboxSentUnconfirmed OutboxStatus = "sent_unconfirmed"
	OutboxDelivered       OutboxStatus = "delivered"
	OutboxRetryable       OutboxStatus = "retryable"
	OutboxQuarantined     OutboxStatus = "quarantined"
	OutboxDead            OutboxStatus = "dead"
)

// AttemptOutcome is the result of one publish attempt.
type AttemptOutcome string

const (
	AttemptSucceeded        AttemptOutcome = "succeeded"
	AttemptAmbiguous        AttemptOutcome = "ambiguous"
	AttemptTemporaryFailure AttemptOutcome = "temporary_failure"
	AttemptPermanentFailure AttemptOutcome = "permanent_failure"
)

// RepairAction is an operator's manual decision for an outbox item stuck
// sent_unconfirmed or quarantined after Reconcile could not resolve it
// deterministically — see ADR 0030.
type RepairAction string

const (
	// RepairDeliver marks the item Delivered: an operator confirmed by
	// other means (e.g. eyeballing the channel) that Telegram has the
	// message.
	RepairDeliver RepairAction = "deliver"
	// RepairRetry returns the item to Retryable, available immediately:
	// an operator confirmed Telegram does NOT have the message.
	RepairRetry RepairAction = "retry"
	// RepairDiscard marks the item Dead: an operator decided this
	// publication should not be attempted or retried further.
	RepairDiscard RepairAction = "discard"
)

// ValidRepairAction reports whether a is a recognized RepairAction.
func ValidRepairAction(a RepairAction) bool {
	switch a {
	case RepairDeliver, RepairRetry, RepairDiscard:
		return true
	default:
		return false
	}
}

// BackoffPolicy computes retry delays and the retry budget for outbox
// publication attempts.
type BackoffPolicy struct {
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	MaxAttempts int
}

// NextAvailableAt returns when the next attempt should run after the
// attempt-th failure (1-indexed), using exponential backoff capped at
// MaxDelay.
func (p BackoffPolicy) NextAvailableAt(now time.Time, attempt int) time.Time {
	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= p.MaxDelay {
			delay = p.MaxDelay
			break
		}
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	return now.Add(delay)
}

// Exhausted reports whether attempt has used up the configured retry budget.
func (p BackoffPolicy) Exhausted(attempt int) bool {
	return attempt >= p.MaxAttempts
}
