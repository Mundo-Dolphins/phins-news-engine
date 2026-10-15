package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/phins-news-engine/app/outboxreconcile"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ReconciliationConfig configures how PublishOutbox.Reconcile resolves
// sent_unconfirmed items whose outcome a durable message_id alone can't
// answer — see ADR 0030.
type ReconciliationConfig struct {
	BatchSize    int           // how many sent_unconfirmed items to reconcile per pass
	ChannelRef   string        // Telegram channel to search (out.TelegramImportRequest.ChannelRef)
	HistoryLimit int           // how many recent channel messages to fetch per pass
	ClockSkew    time.Duration // tolerance between local and Telegram server clocks
	URLConfig    normalization.URLConfig
	URLResolvers []normalization.KnownLinkResolver
}

// PublishOutbox drains the transactional outbox: it claims due items,
// checks each against the publication policy (burst/same-topic/quiet
// hours; deferred items are rescheduled, never discarded),
// publishes the rest, and applies retry/backoff or discards depending on
// the outcome. It runs on its own scheduler interval, independent of
// ProcessCycle.
type PublishOutbox struct {
	articles   out.ArticleRepository
	outbox     out.OutboxRepository
	decisions  out.DecisionRepository
	uow        out.UnitOfWork
	publisher  out.Publisher
	backoff    publishing.BackoffPolicy
	policy     publishing.PolicyConfig
	workerID   string
	batchSize  int
	staleAfter time.Duration
	clock      out.Clock
	logger     out.Logger

	clusterPublications   out.ClusterPublicationRepository
	sourcePolicyCooldowns out.SourcePolicyCooldownRepository

	reconcile       ReconciliationConfig
	channelImporter out.TelegramChannelImporter // nil: reconciliation-by-history unavailable, ambiguous items with no message_id quarantine immediately
}

type telegramRateLimitBatchStop struct {
	retryAt time.Time
}

func (e *telegramRateLimitBatchStop) Error() string {
	return "telegram rate limited"
}

// NewPublishOutbox builds a PublishOutbox use case.
func NewPublishOutbox(
	articles out.ArticleRepository,
	outbox out.OutboxRepository,
	decisions out.DecisionRepository,
	uow out.UnitOfWork,
	publisher out.Publisher,
	backoff publishing.BackoffPolicy,
	policy publishing.PolicyConfig,
	workerID string,
	batchSize int,
	staleAfter time.Duration,
	clock out.Clock,
	logger out.Logger,
	clusterPublications out.ClusterPublicationRepository,
	sourcePolicyCooldowns out.SourcePolicyCooldownRepository,
	reconcile ReconciliationConfig,
	channelImporter out.TelegramChannelImporter,
) *PublishOutbox {
	return &PublishOutbox{
		articles:   articles,
		outbox:     outbox,
		decisions:  decisions,
		uow:        uow,
		publisher:  publisher,
		backoff:    backoff,
		policy:     policy,
		workerID:   workerID,
		batchSize:  batchSize,
		staleAfter: staleAfter,
		clock:      clock,
		logger:     logger,

		clusterPublications:   clusterPublications,
		sourcePolicyCooldowns: sourcePolicyCooldowns,

		reconcile:       reconcile,
		channelImporter: channelImporter,
	}
}

// Run implements in.PublishOutbox.
func (p *PublishOutbox) Run(ctx context.Context) (in.PublishOutboxResult, error) {
	var result in.PublishOutboxResult
	runID := uuid.NewString()

	reconciled, err := p.Reconcile(ctx)
	if err != nil {
		return result, fmt.Errorf("publish outbox: reconcile: %w", err)
	}
	result.ReconciledPositive = reconciled.ReconciledPositive
	result.ReconciledNegative = reconciled.ReconciledNegative
	result.Quarantined = reconciled.Quarantined

	if err := p.expireOverdue(ctx, runID, &result); err != nil {
		return result, err
	}

	now := p.clock.Now()
	items, err := p.outbox.ClaimBatch(ctx, p.workerID, p.batchSize, p.staleAfter, now)
	if err != nil {
		return result, fmt.Errorf("publish outbox: claim batch: %w", err)
	}
	result.Claimed = len(items)

	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := p.publishOne(ctx, runID, item, &result); err != nil {
			var rateLimitStop *telegramRateLimitBatchStop
			if errors.As(err, &rateLimitStop) {
				if err := p.deferRemainingAfterTelegramRateLimit(ctx, runID, items[i+1:], rateLimitStop.retryAt, &result); err != nil {
					return result, err
				}
				break
			}
			return result, err
		}
	}

	p.logger.Info(
		"publish outbox completed",
		"run_id", runID,
		"claimed", result.Claimed, "published", result.Published,
		"retried", result.Retried, "discarded", result.Discarded, "expired", result.Expired, "deferred", result.Deferred,
		"ambiguous_detected", result.AmbiguousDetected,
		"reconciled_positive", result.ReconciledPositive, "reconciled_negative", result.ReconciledNegative,
		"quarantined", result.Quarantined,
		"cooldown_confirmed", result.CooldownConfirmed,
		"cooldown_released", result.CooldownReleased,
	)
	return result, nil
}

func (p *PublishOutbox) publishOne(ctx context.Context, runID string, item out.OutboxItem, result *in.PublishOutboxResult) error {
	a, err := p.articles.FindByID(ctx, item.ArticleID)
	if err != nil {
		return fmt.Errorf("publish outbox: find article %s: %w", item.ArticleID, err)
	}

	now := p.clock.Now()
	// A non-NULL expiry is the admission-time freshness decision. It is
	// intentionally independent from available_at and from any later article
	// revalidation; the latter remains the compatibility path for legacy rows
	// whose expiry could not be reconstructed.
	if (!item.ExpiresAt.IsZero() && !now.Before(item.ExpiresAt)) ||
		(item.ExpiresAt.IsZero() && !publishing.DecideStored(a, now)) {
		return p.expireItem(ctx, runID, item, a, result)
	}
	decision, err := p.evaluatePolicy(ctx, a, item.Lane, now)
	if err != nil {
		return fmt.Errorf("publish outbox: evaluate policy for %s: %w", a.ID, err)
	}
	if !decision.Allow {
		return p.deferItem(ctx, runID, item, a, decision, result)
	}

	a.Status = article.StatusPublishing
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("publish outbox: mark %s publishing: %w", a.ID, err)
	}

	// The network call happens with no database transaction open — never
	// hold a SQLite transaction across a slow, blocking external call.
	start := p.clock.Now()
	messageID, publishErr := p.publisher.Publish(ctx, a)
	duration := p.clock.Now().Sub(start)

	var ambiguous *out.AmbiguousError
	switch {
	case publishErr == nil:
		return p.recordSentThenFinalize(ctx, runID, item, a, messageID, duration, result)
	case errors.As(publishErr, &ambiguous):
		return p.recordAmbiguous(ctx, runID, item, a, ambiguous, duration, result)
	default:
		return p.recordFailure(ctx, runID, item, a, publishErr, duration, result)
	}
}

const (
	// PublishExpiredEventType is the audit event for freshness expiry at the
	// publication edge.
	PublishExpiredEventType = "outbox_expired"
	// PublishExpiredReasonCode distinguishes expiry from a Telegram failure.
	PublishExpiredReasonCode = "publish_expired"
)

// expireItem closes the pre-network freshness race in one short transaction.
// It records no Attempt because the item never reached Telegram.
func (p *PublishOutbox) expireItem(ctx context.Context, runID string, item out.OutboxItem, a article.Article, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	fromStatus := a.Status
	a.Status = article.StatusDiscarded
	a.StatusReason = PublishExpiredReasonCode
	err := p.uow.Do(ctx, func(txCtx context.Context) error {
		if err := p.outbox.ExpireLeased(txCtx, item.ID, PublishExpiredReasonCode, now); err != nil {
			return fmt.Errorf("expire outbox item: %w", err)
		}
		if err := p.articles.Save(txCtx, a); err != nil {
			return fmt.Errorf("save expired article: %w", err)
		}
		if a.ClusterID != "" {
			if err := p.clusterPublications.Release(txCtx, a.ClusterID, a.ID, now); err != nil {
				return fmt.Errorf("release cluster publication gate: %w", err)
			}
			if affected, err := p.sourcePolicyCooldowns.Release(txCtx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
				return fmt.Errorf("release source policy cooldown: %w", err)
			} else if affected {
				result.CooldownReleased++
			}
		}
		return p.decisions.Append(txCtx, out.Event{
			ArticleID: a.ID, OccurredAt: now, FromStatus: fromStatus, ToStatus: article.StatusDiscarded,
			EventType: PublishExpiredEventType, ReasonCode: PublishExpiredReasonCode,
			ReasonDetail: fmt.Sprintf("published_at=%s freshness_max_age=%s", a.PublishedAt.Format(time.RFC3339Nano), a.FreshnessMaxAge),
		})
	})
	if err != nil {
		return fmt.Errorf("publish outbox: persist expiration for %s: %w", a.ID, err)
	}
	p.logger.Info("outbox item expired before publish", "run_id", runID, "article_id", a.ID, "outbox_id", item.ID, "lane", item.Lane.Normalized(), "reason", PublishExpiredReasonCode)
	result.Expired++
	return nil
}

// expiredSweepLimit bounds how many overdue items one pass expires. These
// never reach the network, so it can be well above the publish batch size.
const expiredSweepLimit = 200

// expireOverdue expires pending/retryable items whose known deadline has
// passed. ClaimBatch never selects them (ADR 0007), so without this sweep
// an item deferred past its deadline would sit pending forever.
func (p *PublishOutbox) expireOverdue(ctx context.Context, runID string, result *in.PublishOutboxResult) error {
	items, err := p.outbox.ClaimExpired(ctx, p.workerID, expiredSweepLimit, p.clock.Now())
	if err != nil {
		return fmt.Errorf("publish outbox: claim expired: %w", err)
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		a, err := p.articles.FindByID(ctx, item.ArticleID)
		if err != nil {
			return fmt.Errorf("publish outbox: find expired article %s: %w", item.ArticleID, err)
		}
		if err := p.expireItem(ctx, runID, item, a, result); err != nil {
			return err
		}
	}
	return nil
}

// evaluatePolicy gathers the counts publishing.Evaluate needs and asks it
// whether a may publish now.
func (p *PublishOutbox) evaluatePolicy(ctx context.Context, a article.Article, lane publishing.PublicationLane, now time.Time) (publishing.PolicyDecision, error) {
	lane = lane.Normalized()
	policy := p.policy
	if lane == publishing.LaneLive && p.policy.Live != nil {
		policy = *p.policy.Live
	}
	var burstCount int
	var err error
	if laneCounts, ok := p.outbox.(out.LaneCountRepository); ok {
		burstCount, err = laneCounts.CountSucceededSinceLane(ctx, lane, now.Add(-policy.BurstWindow))
	} else {
		burstCount, err = p.outbox.CountSucceededSince(ctx, now.Add(-policy.BurstWindow))
	}
	if err != nil {
		return publishing.PolicyDecision{}, fmt.Errorf("count succeeded since: %w", err)
	}

	var topicCount int
	if policy.SameTopicApplies(a.PrimaryEntity) {
		if laneCounts, ok := p.outbox.(out.LaneCountRepository); ok {
			topicCount, err = laneCounts.CountSucceededByPrimaryEntitySinceLane(ctx, lane, a.PrimaryEntity, now.Add(-policy.SameTopicWindow))
		} else {
			topicCount, err = p.outbox.CountSucceededByPrimaryEntitySince(ctx, a.PrimaryEntity, now.Add(-policy.SameTopicWindow))
		}
		if err != nil {
			return publishing.PolicyDecision{}, fmt.Errorf("count succeeded for entity %q: %w", a.PrimaryEntity, err)
		}
	}

	return publishing.Evaluate(publishing.PolicyInput{
		PrimaryEntity:              a.PrimaryEntity,
		PublishedInBurstWindow:     burstCount,
		PublishedForEntityInWindow: topicCount,
	}, policy, now), nil
}

// deferItem reschedules item per decision.RetryAt without touching the
// article's status or the retry budget: policy deferral is
// never a failure.
func (p *PublishOutbox) deferItem(ctx context.Context, runID string, item out.OutboxItem, a article.Article, decision publishing.PolicyDecision, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	if err := p.outbox.Defer(ctx, item.ID, decision.RetryAt, now); err != nil {
		return fmt.Errorf("publish outbox: defer %s: %w", a.ID, err)
	}
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: a.Status, ToStatus: a.Status,
		EventType: "publish_deferred", ReasonCode: decision.Reason,
		ReasonDetail: fmt.Sprintf("deferred until %s: %s", decision.RetryAt.Format(time.RFC3339), decision.Reason),
	}); err != nil {
		return fmt.Errorf("publish outbox: append deferred event for %s: %w", a.ID, err)
	}
	p.logger.Info("publish deferred", "run_id", runID, "article_id", a.ID, "lane", item.Lane.Normalized(), "reason", decision.Reason, "retry_at", decision.RetryAt)
	result.Deferred++
	return nil
}

func (p *PublishOutbox) deferRemainingAfterTelegramRateLimit(ctx context.Context, runID string, items []out.OutboxItem, retryAt time.Time, result *in.PublishOutboxResult) error {
	if len(items) == 0 {
		return nil
	}
	now := p.clock.Now()
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.outbox.Defer(ctx, item.ID, retryAt, now); err != nil {
			return fmt.Errorf("publish outbox: defer %s after telegram rate limit: %w", item.ArticleID, err)
		}
		if err := p.decisions.Append(ctx, out.Event{
			ArticleID: item.ArticleID, OccurredAt: now, FromStatus: article.StatusPendingPublication, ToStatus: article.StatusPendingPublication,
			EventType: "publish_deferred", ReasonCode: "telegram_rate_limit",
			ReasonDetail: fmt.Sprintf("deferred until %s: telegram_rate_limit", retryAt.Format(time.RFC3339)),
		}); err != nil {
			return fmt.Errorf("publish outbox: append rate-limit deferred event for %s: %w", item.ArticleID, err)
		}
		result.Deferred++
	}
	p.logger.Warn("telegram rate limit hit; deferred remaining claimed outbox items",
		"run_id", runID, "deferred", len(items), "retry_at", retryAt)
	return nil
}

// recordSentThenFinalize is fault-injection point 3 from ADR 0030: it
// durably records Telegram's own proof of delivery (messageID) BEFORE
// doing anything else. If the process dies immediately after
// RecordSentUnconfirmed returns, the item is left status='sent_unconfirmed'
// with SentMessageID set — Reconcile finalizes it on the very next Run,
// with no re-send, because that recorded message_id already IS the proof.
func (p *PublishOutbox) recordSentThenFinalize(ctx context.Context, runID string, item out.OutboxItem, a article.Article, messageID string, duration time.Duration, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	// Any error here — including out.ErrLeaseLost, meaning the row moved
	// out from under this in-flight send — aborts this item. Telegram has
	// already accepted the message; carrying on without durable evidence
	// of that is exactly the blind-resend window this feature closes, so
	// this must be loud, not silent.
	if err := p.outbox.RecordSentUnconfirmed(ctx, item.ID, messageID, "", now); err != nil {
		return fmt.Errorf("publish outbox: record sent-unconfirmed evidence for %s: %w", a.ID, err)
	}
	return p.finalizeDelivered(ctx, runID, item, a, messageID, duration, result)
}

// finalizeDelivered is the durable "this is definitely delivered"
// transaction — shared by the synchronous-success path above and by
// Reconcile's positive paths, which call it with duration=0 (no publish
// attempt happened in that call).
func (p *PublishOutbox) finalizeDelivered(ctx context.Context, runID string, item out.OutboxItem, a article.Article, messageID string, duration time.Duration, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	a.Status = article.StatusPublished
	a.StatusReason = ""

	err := p.uow.Do(ctx, func(txCtx context.Context) error {
		if err := p.articles.Save(txCtx, a); err != nil {
			return fmt.Errorf("save article: %w", err)
		}
		// out.ErrLeaseLost here rolls the whole finalize transaction back:
		// better to leave the item for Reconcile (which has the recorded
		// message_id to work from) than to record a delivery whose outbox
		// row never actually left the claimable states.
		if err := p.outbox.MarkDelivered(txCtx, item.ID, now); err != nil {
			return fmt.Errorf("mark delivered: %w", err)
		}
		if err := p.outbox.RecordAttempt(txCtx, out.Attempt{
			OutboxID: item.ID, ArticleID: a.ID, AttemptedAt: now,
			Outcome: publishing.AttemptSucceeded, Duration: duration, ExternalMessageID: messageID,
		}); err != nil {
			return fmt.Errorf("record attempt: %w", err)
		}
		if a.ClusterID != "" {
			if err := p.clusterPublications.Confirm(txCtx, a.ClusterID, a.ID, item.ID, now); err != nil {
				return fmt.Errorf("confirm cluster publication gate: %w", err)
			}
			if affected, err := p.sourcePolicyCooldowns.Confirm(txCtx, a.Source.ID.String(), a.ClusterID, a.ID, item.ID, now); err != nil {
				return fmt.Errorf("confirm source policy cooldown: %w", err)
			} else if affected {
				result.CooldownConfirmed++
			}
		}
		return p.decisions.Append(txCtx, out.Event{
			ArticleID: a.ID, OccurredAt: now, FromStatus: article.StatusPublishing,
			ToStatus: article.StatusPublished, EventType: "publish",
		})
	})
	if err != nil {
		return fmt.Errorf("publish outbox: persist delivered for %s: %w", a.ID, err)
	}
	p.logger.Info("article published", "run_id", runID, "article_id", a.ID, "cluster_id", a.ClusterID,
		"source", a.Source.ID, "lane", item.Lane.Normalized(), "latency_ms", duration.Milliseconds(), "retry_count", item.AttemptCount)
	result.Published++
	return nil
}

// recordAmbiguous is fault-injection point 2: Publish returned
// *out.AmbiguousError — whether Telegram processed the request is
// genuinely unknown. It records that evidence and stops: no retry, no
// finalize. The item surfaces only through Reconcile from here.
func (p *PublishOutbox) recordAmbiguous(ctx context.Context, runID string, item out.OutboxItem, a article.Article, ambErr *out.AmbiguousError, duration time.Duration, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	reason := ambErr.Error()
	// As in recordSentThenFinalize: a lost lease (out.ErrLeaseLost) here
	// means the ambiguity was never recorded, so the item stays claimable
	// and would be resent with its outcome still unknown. Abort loudly.
	if err := p.outbox.RecordSentUnconfirmed(ctx, item.ID, "", reason, now); err != nil {
		return fmt.Errorf("publish outbox: record ambiguous evidence for %s: %w", a.ID, err)
	}
	if err := p.outbox.RecordAttempt(ctx, out.Attempt{
		OutboxID: item.ID, ArticleID: a.ID, AttemptedAt: now,
		Outcome: publishing.AttemptAmbiguous, ErrorMessage: reason, Duration: duration,
	}); err != nil {
		return fmt.Errorf("publish outbox: record ambiguous attempt for %s: %w", a.ID, err)
	}
	p.logger.Warn("publish outcome ambiguous, holding for reconciliation",
		"run_id", runID, "article_id", a.ID, "outbox_id", item.ID, "lane", item.Lane.Normalized(), "reason", reason)
	result.AmbiguousDetected++
	return nil
}

func (p *PublishOutbox) recordFailure(ctx context.Context, runID string, item out.OutboxItem, a article.Article, publishErr error, duration time.Duration, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	var permErr *out.PermanentError
	permanent := errors.As(publishErr, &permErr)
	nextAttempt := item.AttemptCount + 1
	dead := permanent || p.backoff.Exhausted(nextAttempt)

	a.Status = article.StatusPendingRetry
	if dead {
		a.Status = article.StatusDiscarded
	}
	a.StatusReason = publishErr.Error()

	nextAvailableAt := p.backoff.NextAvailableAt(now, nextAttempt)
	var rateLimited *out.RateLimitedError
	if errors.As(publishErr, &rateLimited) {
		if suggested := now.Add(rateLimited.RetryAfter); suggested.After(nextAvailableAt) {
			nextAvailableAt = suggested
		}
	}
	outcome := publishing.AttemptTemporaryFailure
	if permanent {
		outcome = publishing.AttemptPermanentFailure
	}

	err := p.uow.Do(ctx, func(txCtx context.Context) error {
		if err := p.articles.Save(txCtx, a); err != nil {
			return fmt.Errorf("save article: %w", err)
		}
		if err := p.outbox.MarkRetryable(txCtx, item.ID, publishErr.Error(), nextAvailableAt, dead, now); err != nil {
			return fmt.Errorf("mark retryable: %w", err)
		}
		if dead && a.ClusterID != "" {
			if err := p.clusterPublications.Release(txCtx, a.ClusterID, a.ID, now); err != nil {
				return fmt.Errorf("release cluster publication gate: %w", err)
			}
			if affected, err := p.sourcePolicyCooldowns.Release(txCtx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
				return fmt.Errorf("release source policy cooldown: %w", err)
			} else if affected {
				result.CooldownReleased++
			}
		}
		if err := p.outbox.RecordAttempt(txCtx, out.Attempt{
			OutboxID: item.ID, ArticleID: a.ID, AttemptedAt: now,
			Outcome: outcome, ErrorMessage: publishErr.Error(), Duration: duration,
		}); err != nil {
			return fmt.Errorf("record attempt: %w", err)
		}
		return p.decisions.Append(txCtx, out.Event{
			ArticleID: a.ID, OccurredAt: now, FromStatus: article.StatusPublishing,
			ToStatus: a.Status, EventType: "publish_failed", ReasonDetail: publishErr.Error(),
		})
	})
	if err != nil {
		return fmt.Errorf("publish outbox: persist failure for %s: %w", a.ID, err)
	}

	if dead {
		result.Discarded++
		p.logger.Warn("outbox item discarded", "run_id", runID, "article_id", a.ID, "lane", item.Lane.Normalized(), "permanent", permanent, "attempts", nextAttempt)
	} else {
		result.Retried++
		p.logger.Info("publish retry scheduled", "run_id", runID, "article_id", a.ID, "lane", item.Lane.Normalized(), "attempts", nextAttempt, "next_available_at", nextAvailableAt)
	}
	if rateLimited != nil {
		return &telegramRateLimitBatchStop{retryAt: nextAvailableAt}
	}
	return nil
}

// Reconcile resolves every sent_unconfirmed outbox item it can,
// deterministically — see ADR 0030:
//   - a recorded SentMessageID is Telegram's own proof of delivery:
//     finalize immediately, no network call.
//   - otherwise, if a channel-history importer is configured, search
//     recent channel history for the article's canonical URL. A
//     confident match finalizes; a confident non-match (the search
//     covered back past the send time and found nothing) schedules a
//     retry; anything inconclusive — no importer configured, the lookup
//     failed, or the search didn't cover the required window —
//     quarantines the item. Never resent blindly.
//
// Called both from Run (every tick, before claiming new work) and
// directly by the CLI's `telegram outbox reconcile` command.
func (p *PublishOutbox) Reconcile(ctx context.Context) (in.ReconcileResult, error) {
	var result in.ReconcileResult
	now := p.clock.Now()
	items, err := p.outbox.ListSentUnconfirmed(ctx, p.reconcile.BatchSize, now)
	if err != nil {
		return result, fmt.Errorf("reconcile outbox: list sent-unconfirmed: %w", err)
	}

	// The channel-history fetch is expensive (a real MTProto call) and is
	// the same fetch for every item in this pass — fetched at most once,
	// lazily on first need, rather than once per item. Without this, N
	// sent_unconfirmed items with no local message_id would trigger N
	// full history fetches against the same channel per tick, a real
	// flood-wait risk whose failure mode (quarantine) needlessly turns a
	// transient rate limit into manual operator work.
	var (
		historyFetched bool
		messages       []telegramimport.Message
		fetchErr       error
	)

	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if item.SentMessageID == "" && p.channelImporter != nil && !historyFetched {
			messages, fetchErr = p.channelImporter.ExportChannelMessages(ctx, out.TelegramImportRequest{
				ChannelRef: p.reconcile.ChannelRef, Limit: p.reconcile.HistoryLimit,
			})
			historyFetched = true
		}
		if err := p.reconcileOne(ctx, item, now, messages, fetchErr, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (p *PublishOutbox) reconcileOne(ctx context.Context, item out.OutboxItem, now time.Time, messages []telegramimport.Message, fetchErr error, result *in.ReconcileResult) error {
	result.Checked++
	a, err := p.articles.FindByID(ctx, item.ArticleID)
	if err != nil {
		return fmt.Errorf("reconcile outbox: find article %s: %w", item.ArticleID, err)
	}

	if item.SentMessageID != "" {
		if err := p.finalizeDelivered(ctx, "reconcile", item, a, item.SentMessageID, 0, &in.PublishOutboxResult{}); err != nil {
			return err
		}
		p.logger.Info("ambiguous delivery reconciled positive (local evidence)", "outbox_id", item.ID, "article_id", a.ID)
		result.ReconciledPositive++
		return nil
	}

	if p.channelImporter == nil {
		return p.quarantineItem(ctx, item, "reconciliation_unavailable: no channel history importer configured", result)
	}

	if fetchErr != nil {
		p.logger.Warn("channel history reconciliation failed", "outbox_id", item.ID, "article_id", a.ID, "error", fetchErr.Error())
		return p.quarantineItem(ctx, item, "channel_history_lookup_failed", result)
	}

	messageID, matched, covered := outboxreconcile.MatchInHistory(messages, a.CanonicalURL, item.SentAt, p.reconcile.URLConfig, p.reconcile.URLResolvers, p.reconcile.ClockSkew)
	switch {
	case matched:
		// Same rule as the publish path: if this write finds no row to
		// update (out.ErrLeaseLost) the match evidence isn't durable, so
		// abort rather than finalize on top of a no-op.
		if err := p.outbox.RecordSentUnconfirmed(ctx, item.ID, messageID, "reconciled_from_channel_history", now); err != nil {
			return fmt.Errorf("reconcile outbox: record matched message id for %s: %w", a.ID, err)
		}
		if err := p.finalizeDelivered(ctx, "reconcile", item, a, messageID, 0, &in.PublishOutboxResult{}); err != nil {
			return err
		}
		p.logger.Info("ambiguous delivery reconciled positive (channel history)", "outbox_id", item.ID, "article_id", a.ID)
		result.ReconciledPositive++
		return nil
	case covered:
		return p.reconcileNegative(ctx, item, a, now, result)
	default:
		return p.quarantineItem(ctx, item, "channel_history_inconclusive", result)
	}
}

// reconcileNegative is the confirmed-not-delivered outcome: channel
// history search reached back far enough to trust the absence and found
// nothing. It mirrors recordFailure's transaction shape (article status,
// RecordAttempt, decision event) rather than only calling MarkRetryable,
// so a reconciled-negative item is exactly as auditable in
// publication_attempts and the decision log as any other failed publish
// attempt — the whole point of this feature is making an ambiguous
// outcome's eventual resolution visible, not just recoverable.
func (p *PublishOutbox) reconcileNegative(ctx context.Context, item out.OutboxItem, a article.Article, now time.Time, result *in.ReconcileResult) error {
	reason := "reconciled: not found in channel history"
	nextAvailableAt := p.backoff.NextAvailableAt(now, item.AttemptCount+1)

	a.Status = article.StatusPendingRetry
	a.StatusReason = reason

	err := p.uow.Do(ctx, func(txCtx context.Context) error {
		if err := p.articles.Save(txCtx, a); err != nil {
			return fmt.Errorf("save article: %w", err)
		}
		if err := p.outbox.MarkRetryable(txCtx, item.ID, reason, nextAvailableAt, false, now); err != nil {
			return fmt.Errorf("mark retryable: %w", err)
		}
		if err := p.outbox.RecordAttempt(txCtx, out.Attempt{
			OutboxID: item.ID, ArticleID: a.ID, AttemptedAt: now,
			Outcome: publishing.AttemptTemporaryFailure, ErrorMessage: reason,
		}); err != nil {
			return fmt.Errorf("record attempt: %w", err)
		}
		return p.decisions.Append(txCtx, out.Event{
			ArticleID: a.ID, OccurredAt: now, FromStatus: article.StatusPublishing,
			ToStatus: article.StatusPendingRetry, EventType: "reconcile_negative", ReasonDetail: reason,
		})
	})
	if err != nil {
		return fmt.Errorf("reconcile outbox: mark retryable for %s: %w", a.ID, err)
	}
	p.logger.Info("ambiguous delivery reconciled negative", "outbox_id", item.ID, "article_id", a.ID)
	result.ReconciledNegative++
	return nil
}

func (p *PublishOutbox) quarantineItem(ctx context.Context, item out.OutboxItem, reason string, result *in.ReconcileResult) error {
	if err := p.outbox.Quarantine(ctx, item.ID, reason, p.clock.Now()); err != nil {
		return fmt.Errorf("reconcile outbox: quarantine %d: %w", item.ID, err)
	}
	p.logger.Warn("ambiguous delivery quarantined, needs manual reconciliation",
		"outbox_id", item.ID, "article_id", item.ArticleID, "reason", reason)
	result.Quarantined++
	return nil
}
