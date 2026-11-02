package out

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// EventClaimState is where a claim is in its life.
type EventClaimState string

const (
	// EventClaimClaimed: the key is reserved and its publication is pending.
	EventClaimClaimed EventClaimState = "claimed"
	// EventClaimPublished: the publication behind the claim was delivered.
	EventClaimPublished EventClaimState = "published"
	// EventClaimFailed: the publication ended permanently undelivered. The
	// key stays taken until an operator releases it.
	EventClaimFailed EventClaimState = "failed"
)

// Valid reports whether s is one of the known states.
func (s EventClaimState) Valid() bool {
	switch s {
	case EventClaimClaimed, EventClaimPublished, EventClaimFailed:
		return true
	}
	return false
}

var (
	// ErrEventClaimNotFound is returned when no claim exists for the key.
	ErrEventClaimNotFound = errors.New("event claim not found")
	// ErrEventClaimPublished is returned by Release for a published claim:
	// releasing it would allow a duplicate post.
	ErrEventClaimPublished = errors.New("event claim is published and cannot be released")
)

// EventClaim is one stored claim.
type EventClaim struct {
	ID        int64
	Handler   string
	Key       string
	State     EventClaimState
	ArticleID string
	// Data is handler-specific JSON, opaque to the engine. Never empty: a
	// claim stored without data reads back as {}.
	Data      json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EventClaimFilter narrows List. Zero fields match everything.
type EventClaimFilter struct {
	Handler string
	State   EventClaimState
	// ArticleID restricts to the claims taken for one article.
	ArticleID string
	// Limit caps the rows returned, newest first; zero means no cap.
	Limit int
}

// EventClaimRepository is the transactional idempotency store event handlers
// use to reserve a unique key before acting (ADR 0034). Uniqueness of
// (handler, key) is enforced by the database.
//
// Every method joins the unit of work on ctx when there is one, so a claim
// written by a handler during ProcessCycle commits or rolls back with the
// article and its outbox row.
type EventClaimRepository interface {
	// TryClaim atomically reserves key for handler. It returns true when
	// this call took the key, false when it already existed in any state.
	// data must be valid JSON or empty. A handler calls it only after its
	// own validations pass, so a rejected report never takes the key.
	TryClaim(ctx context.Context, handler, key, articleID string, data json.RawMessage, now time.Time) (claimed bool, err error)
	// MarkPublished moves a claim to published. ErrEventClaimNotFound if the
	// claim does not exist.
	MarkPublished(ctx context.Context, handler, key string, now time.Time) error
	// MarkFailed moves a claim to failed. ErrEventClaimNotFound if the claim
	// does not exist.
	MarkFailed(ctx context.Context, handler, key string, now time.Time) error
	// Release deletes a claimed or failed claim so the key can be claimed
	// again, and reports whether a row was deleted. A published claim is
	// never released (ErrEventClaimPublished).
	Release(ctx context.Context, handler, key string) (released bool, err error)
	// Get returns the claim, with found false when there is none.
	Get(ctx context.Context, handler, key string) (claim EventClaim, found bool, err error)
	// List returns claims matching the filter, newest first.
	List(ctx context.Context, filter EventClaimFilter) ([]EventClaim, error)
	// ReconcileWithOutbox settles every claimed claim whose article has
	// outbox rows that are all finished: failed when any row is dead,
	// published when at least one was delivered. It lets the outbox stay
	// unaware of handlers. It returns how many claims moved.
	ReconcileWithOutbox(ctx context.Context, now time.Time) (published, failed int, err error)
}

var (
	// ErrEventClaimNotFailed is returned by RetryFailed for a claim that is
	// not in the failed state.
	ErrEventClaimNotFailed = errors.New("event claim is not failed")
	// ErrEventClaimInFlight is returned by RetryFailed when an outbox row of
	// the claim's article is still being worked on, or needs `outbox repair`.
	ErrEventClaimInFlight = errors.New("event claim has an in-flight outbox row")
	// ErrEventClaimNothingToRetry is returned by RetryFailed when the failed
	// claim has no dead outbox row to re-enqueue (for example its action was
	// never persisted). Release the claim instead.
	ErrEventClaimNothingToRetry = errors.New("event claim has no dead outbox row to retry")
)

// EventClaimOutboxRow is the outbox state of one destination of a claim's
// article, as the operator CLI shows it.
type EventClaimOutboxRow struct {
	Destination string
	Status      string
	Attempts    int
	LastError   string
}

// EventClaimOperations is what the operator CLI needs beyond the handler-facing
// EventClaimRepository (ADR 0034). It is a separate interface so host
// implementations of EventClaimRepository are unaffected.
type EventClaimOperations interface {
	// OutboxRows returns the outbox rows of an article, oldest first.
	OutboxRows(ctx context.Context, articleID string) ([]EventClaimOutboxRow, error)
	// RetryFailed re-enqueues the dead outbox rows of a failed claim's
	// article (same destination and payload, attempts reset) and sets the
	// claim back to claimed, in one transaction. It returns how many rows
	// were re-enqueued. ErrEventClaimNotFound, ErrEventClaimNotFailed,
	// ErrEventClaimInFlight and ErrEventClaimNothingToRetry are the refusals.
	RetryFailed(ctx context.Context, handler, key string, now time.Time) (requeued int, err error)
}
