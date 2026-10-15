package out

import (
	"context"
	"errors"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

// OutboxItem is one queued publication.
type OutboxItem struct {
	ID              int64
	ArticleID       string
	Payload         string
	Status          publishing.OutboxStatus
	AvailableAt     time.Time
	ExpiresAt       time.Time
	ClaimedAt       time.Time
	ClaimedBy       string
	AttemptCount    int
	LastError       string
	SentMessageID   string
	SentAt          time.Time
	AmbiguousReason string
	Lane            publishing.PublicationLane
}

// LaneOutboxRepository is optional during the compatibility rollout. The
// legacy Enqueue method always means normal.
type LaneOutboxRepository interface {
	EnqueueLane(ctx context.Context, articleID, payload string, lane publishing.PublicationLane, now time.Time) error
}

type LaneCountRepository interface {
	CountSucceededSinceLane(ctx context.Context, lane publishing.PublicationLane, since time.Time) (int, error)
	CountSucceededByPrimaryEntitySinceLane(ctx context.Context, lane publishing.PublicationLane, entity string, since time.Time) (int, error)
}

// StatusCount summarizes how many outbox items sit in one status, how many
// have crossed their persisted expiry deadline, and how long the oldest has
// been sitting there.
type StatusCount struct {
	Count        int
	ExpiredCount int
	OldestAge    time.Duration
}

// Attempt is one recorded publish attempt.
type Attempt struct {
	OutboxID          int64
	ArticleID         string
	AttemptedAt       time.Time
	Outcome           publishing.AttemptOutcome
	ErrorMessage      string
	ExternalMessageID string
	Duration          time.Duration
}

// OutboxRepository persists the transactional outbox for article
// publication — see ADR 0007 and ADR 0030.
type OutboxRepository interface {
	// Enqueue inserts a pending outbox item for articleID if one doesn't
	// already exist. It is idempotent: calling it twice for the same
	// articleID is a no-op, not an error, so callers never need to check
	// first.
	Enqueue(ctx context.Context, articleID, payload string, now time.Time) error
	// ClaimBatch first reclaims items stuck Leased past staleAfter back to
	// Pending (crash recovery), then atomically claims up to limit items
	// whose status is Pending or Retryable and whose AvailableAt has
	// passed, marking them Leased by workerID, and returns them.
	ClaimBatch(ctx context.Context, workerID string, limit int, staleAfter time.Duration, now time.Time) ([]OutboxItem, error)
	// ClaimExpired leases up to limit Pending or Retryable items whose
	// known ExpiresAt has passed (whatever their AvailableAt), marking them
	// Leased by workerID, so they can be expired through ExpireLeased.
	// ClaimBatch excludes such items; items with no ExpiresAt are never
	// returned (ADR 0007).
	ClaimExpired(ctx context.Context, workerID string, limit int, now time.Time) ([]OutboxItem, error)
	// RecordSentUnconfirmed durably records whatever evidence a publish
	// attempt produced — a message_id on a confirmed send, or an empty
	// message_id with ambiguousReason set on a transport/read/parse
	// failure of unknown outcome — moving item id from Leased to
	// SentUnconfirmed. This is the crash-safety anchor ADR 0030 depends
	// on: once this write commits, ClaimBatch's stale-claim reclaim can
	// never touch the row again, so a crash after this point can never
	// cause a blind resend. Returns ErrLeaseLost if id is in neither
	// state: this write must never silently no-op.
	RecordSentUnconfirmed(ctx context.Context, id int64, messageID, ambiguousReason string, now time.Time) error
	// MarkDelivered marks item id as successfully Delivered. Returns
	// ErrLeaseLost if id is not currently Leased or SentUnconfirmed.
	MarkDelivered(ctx context.Context, id int64, now time.Time) error
	// ExpireLeased moves a claimed item to the terminal dead state because
	// its article is no longer fresh. It must not increment attempt_count or
	// create a publication attempt.
	ExpireLeased(ctx context.Context, id int64, reason string, now time.Time) error
	// MarkRetryable records a failed (or negatively-reconciled) attempt on
	// item id: if dead, the item moves to OutboxDead (retry budget
	// exhausted or a permanent error); otherwise it goes to OutboxRetryable
	// with AvailableAt set to nextAvailableAt, and attempt_count is
	// incremented either way.
	MarkRetryable(ctx context.Context, id int64, errMsg string, nextAvailableAt time.Time, dead bool, now time.Time) error
	// Quarantine moves a SentUnconfirmed item id to OutboxQuarantined:
	// Reconcile could not determine the outcome deterministically, so the
	// item is held for manual repair rather than auto-retried.
	Quarantine(ctx context.Context, id int64, reason string, now time.Time) error
	// Defer returns a claimed item to Pending with AvailableAt set to
	// availableAt, without incrementing attempt_count or counting against
	// the retry budget — used when publication_policy, not a
	// publish failure, is why this item isn't going out right now.
	Defer(ctx context.Context, id int64, availableAt time.Time, now time.Time) error
	// RecordAttempt appends a publication_attempts row for auditability.
	RecordAttempt(ctx context.Context, a Attempt) error
	// DeleteOldAttempts removes publication_attempts rows older than
	// cutoff.
	DeleteOldAttempts(ctx context.Context, cutoff time.Time) (int, error)
	// CountSucceededSince returns how many publication_attempts rows have
	// outcome "succeeded" and attempted_at >= since — the burst-limit input
	// for domain/publishing.Evaluate. Counts by actual send
	// time, not the article's own published_at, since the burst limit
	// bounds how fast this bot sends to Telegram, not how fresh the source
	// article is (that's FreshnessPolicy's separate concern).
	CountSucceededSince(ctx context.Context, since time.Time) (int, error)
	// CountSucceededByPrimaryEntitySince returns how many publication_attempts
	// rows have outcome "succeeded", attempted_at >= since, and belong to an
	// article whose PrimaryEntity == entity — the same-topic-limit input for
	// domain/publishing.Evaluate. Same actual-send-time rationale
	// as CountSucceededSince.
	CountSucceededByPrimaryEntitySince(ctx context.Context, entity string, since time.Time) (int, error)
	// ListSentUnconfirmed returns up to limit SentUnconfirmed items,
	// oldest sent_at first — Reconcile's input for deterministic
	// resolution.
	ListSentUnconfirmed(ctx context.Context, limit int, now time.Time) ([]OutboxItem, error)
	// ListQuarantined returns up to limit Quarantined items, oldest
	// quarantined_at first — the CLI's `telegram outbox repair` listing.
	ListQuarantined(ctx context.Context, limit int) ([]OutboxItem, error)
	// Repair applies an operator's manual decision (action) to outbox item
	// id: it only ever transitions an item currently SentUnconfirmed or
	// Quarantined, and returns ErrNotRepairable if id is in neither state
	// OR if articleID does not match the item's own article_id — a
	// mismatch here would otherwise mutate a different, unrelated article
	// (and its cluster-publication gate) than the one an operator meant
	// to repair, so a copy/paste slip on --article-id fails loudly
	// instead of silently corrupting the wrong article.
	Repair(ctx context.Context, id int64, articleID string, action publishing.RepairAction, reason string, now time.Time) error
	// StatusCounts returns, per status, how many outbox items are in it
	// and the age of the oldest such row's created_at — a coarse
	// operator-facing summary for the CLI status command.
	StatusCounts(ctx context.Context, now time.Time) (map[publishing.OutboxStatus]StatusCount, error)
}

// ErrNotRepairable is returned by Repair when id is not currently
// sent_unconfirmed or quarantined (RowsAffected == 0) — the CLI surfaces
// this as a clear "nothing to repair" message instead of a silent no-op.
var ErrNotRepairable = errors.New("out: outbox item is not sent_unconfirmed or quarantined")

// ErrLeaseLost is returned by RecordSentUnconfirmed and MarkDelivered when
// their status-guarded UPDATE matches no row (RowsAffected == 0): the item
// was moved out from under an in-flight send, so this process no longer
// owns it. RecordSentUnconfirmed is the crash-safety anchor ADR 0030
// depends on — if that write does not land, the evidence that Telegram may
// already have the message does not exist, and the item can be reclaimed
// and resent blindly. A silent no-op there is precisely the failure mode
// this feature exists to prevent, so it must abort the item loudly rather
// than let the caller proceed as if it succeeded.
var ErrLeaseLost = errors.New("out: outbox item is no longer in the expected state — lease lost")

// ErrNotExpirable means an operator-selected item is no longer claimable
// (or its article id did not match). The whole expiration batch must abort.
var ErrNotExpirable = errors.New("out: outbox item is not pending or retryable")
