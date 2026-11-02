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
	publishers map[publishing.Destination]out.Publisher
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
	eventClaims     out.EventClaimRepository    // nil: event claims are not settled from outbox outcomes
	channelImporter out.TelegramChannelImporter // nil: reconciliation-by-history unavailable, ambiguous items with no message_id quarantine immediately
	alerter         out.Alerter                 // nil: dead and quarantined non-Telegram rows are only logged
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
		publishers: map[publishing.Destination]out.Publisher{publishing.DestinationTelegram: publisher},
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

// SetEventClaims is called by the composition root. Its zero value (nil) is
// safe: event claims are then never settled from outbox outcomes.
func (p *PublishOutbox) SetEventClaims(claims out.EventClaimRepository) { p.eventClaims = claims }

// SetAlerter is called by the composition root. Its zero value (nil) is safe:
// dead and quarantined non-Telegram rows are then only logged.
func (p *PublishOutbox) SetAlerter(alerter out.Alerter) { p.alerter = alerter }

// alertItem tells the operator a non-Telegram row ended dead or quarantined
// (ADR 0037). Telegram is the news channel itself; its failures already show
// up there. Delivery problems are logged, never returned.
func (p *PublishOutbox) alertItem(ctx context.Context, kind string, item out.OutboxItem, lastError string) {
	destination := item.Destination.Normalized()
	if p.alerter == nil || destination == publishing.DestinationTelegram {
		return
	}
	details := fmt.Sprintf("destination: %s\narticle: %s\noutbox id: %d\nlast error: %s", destination, item.ArticleID, item.ID, lastError)
	if p.eventClaims != nil {
		claims, err := p.eventClaims.List(ctx, out.EventClaimFilter{ArticleID: item.ArticleID, Limit: 3})
		if err != nil {
			p.logger.Warn("alert: look up event claims failed", "article_id", item.ArticleID, "error", err.Error())
		}
		for _, c := range claims {
			details += fmt.Sprintf("\nclaim: %s/%s (%s)", c.Handler, c.Key, c.State)
		}
	}
	sendAlert(ctx, p.alerter, p.logger, out.Alert{
		Source:   "outbox",
		Severity: out.AlertCritical,
		Title:    fmt.Sprintf("Publication to %s is %s", destination, kind),
		Details:  details,
		DedupKey: fmt.Sprintf("outbox:%s:%s:%s", destination, kind, reasonKey(lastError)),
	})
}

// RegisterPublisher adds the publisher for a non-Telegram destination
// (ADR 0031). The Telegram publisher is the constructor's and cannot be
// replaced. It is wiring-time only: call it before the first Run.
func (p *PublishOutbox) RegisterPublisher(destination publishing.Destination, publisher out.Publisher) error {
	if err := publishing.ValidateDestination(destination); err != nil {
		return fmt.Errorf("publish outbox: register publisher: %w", err)
	}
	destination = destination.Normalized()
	if publisher == nil {
		return fmt.Errorf("publish outbox: register publisher: nil publisher for destination %q", destination)
	}
	if _, exists := p.publishers[destination]; exists {
		return fmt.Errorf("publish outbox: register publisher: destination %q already has a publisher", destination)
	}
	p.publishers[destination] = publisher
	return nil
}

// ValidateWiring fails when the outbox already holds unfinished work for a
// destination with no registered publisher, so a misconfigured host stops at
// startup instead of wedging at the first claim. It is a no-op for
// repositories without destination support.
func (p *PublishOutbox) ValidateWiring(ctx context.Context) error {
	dr, ok := p.outbox.(out.DestinationOutboxRepository)
	if !ok {
		return nil
	}
	open, err := dr.OpenDestinations(ctx)
	if err != nil {
		return fmt.Errorf("publish outbox: list open destinations: %w", err)
	}
	for _, d := range open {
		if _, ok := p.publishers[d.Normalized()]; !ok {
			return fmt.Errorf("publish outbox: outbox has unfinished %q rows but no publisher is registered for that destination", d)
		}
	}
	return nil
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

	for _, item := range items {
		result.ForDestination(item.Destination).Claimed++
	}
	queue := items
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		item := queue[0]
		queue = queue[1:]
		if err := p.tracked(&result, item.Destination, func() error { return p.publishOne(ctx, runID, item, &result) }); err != nil {
			var rateLimitStop *telegramRateLimitBatchStop
			if errors.As(err, &rateLimitStop) {
				// The rate limit is Telegram's: defer the remaining Telegram
				// rows, but let other destinations keep draining.
				var telegramRest, otherRest []out.OutboxItem
				for _, rest := range queue {
					if rest.Destination.Normalized() == publishing.DestinationTelegram {
						telegramRest = append(telegramRest, rest)
					} else {
						otherRest = append(otherRest, rest)
					}
				}
				if err := p.deferRemainingAfterTelegramRateLimit(ctx, runID, telegramRest, rateLimitStop.retryAt, &result); err != nil {
					return result, err
				}
				queue = otherRest
				continue
			}
			return result, err
		}
	}

	p.settleEventClaims(ctx)

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
	for _, d := range publishing.KnownDestinations() {
		if c, ok := result.ByDestination[d]; ok {
			p.logger.Info(
				"publish outbox destination completed",
				"run_id", runID, "destination", d,
				"claimed", c.Claimed, "published", c.Published, "retried", c.Retried,
				"discarded", c.Discarded, "expired", c.Expired, "deferred", c.Deferred,
				"ambiguous_detected", c.AmbiguousDetected,
			)
		}
	}
	return result, nil
}

// settleEventClaims moves event claims to published or failed once their
// article's outbox rows have finished (ADR 0034), so the outbox itself never
// knows about handlers. A failure is logged, not propagated: the next run
// retries it.
func (p *PublishOutbox) settleEventClaims(ctx context.Context) {
	if p.eventClaims == nil {
		return
	}
	published, failed, err := p.eventClaims.ReconcileWithOutbox(ctx, p.clock.Now())
	if err != nil {
		p.logger.Error("settle event claims failed", err)
		return
	}
	if published+failed > 0 {
		p.logger.Info("event claims settled", "published", published, "failed", failed)
	}
}

// tracked runs fn and attributes the counter deltas it produced to
// destination in result.ByDestination.
func (p *PublishOutbox) tracked(result *in.PublishOutboxResult, destination publishing.Destination, fn func() error) error {
	before := *result
	err := fn()
	c := result.ForDestination(destination)
	c.Published += result.Published - before.Published
	c.Retried += result.Retried - before.Retried
	c.Discarded += result.Discarded - before.Discarded
	c.Expired += result.Expired - before.Expired
	c.Deferred += result.Deferred - before.Deferred
	c.AmbiguousDetected += result.AmbiguousDetected - before.AmbiguousDetected
	return err
}

func (p *PublishOutbox) publishOne(ctx context.Context, runID string, item out.OutboxItem, result *in.PublishOutboxResult) error {
	destination := item.Destination.Normalized()
	publisher, ok := p.publishers[destination]
	if !ok {
		return fmt.Errorf("publish outbox: outbox item %d (article %s) targets destination %q but no publisher is registered for it", item.ID, item.ArticleID, destination)
	}
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
	// The publication policy (burst, same-topic, quiet hours) applies to every
	// destination, counted over that destination's own sends. The article
	// lifecycle belongs to the primary (Telegram) delivery only.
	primary := destination == publishing.DestinationTelegram
	decision, err := p.evaluatePolicy(ctx, a, destination, item.Lane, now)
	if err != nil {
		return fmt.Errorf("publish outbox: evaluate policy for %s: %w", a.ID, err)
	}
	if !decision.Allow {
		return p.deferItem(ctx, runID, item, a, decision, result)
	}
	if primary {
		a.Status = article.StatusPublishing
		if err := p.articles.Save(ctx, a); err != nil {
			return fmt.Errorf("publish outbox: mark %s publishing: %w", a.ID, err)
		}
	}

	var payload publishing.MessagePayload
	payloadPublisher, payloadAware := publisher.(out.PayloadPublisher)
	if payloadAware {
		var perr error
		if payload, perr = publishing.ParseMessagePayload(item.Payload); perr != nil {
			// Retrying cannot repair a malformed payload.
			return p.recordFailure(ctx, runID, item, a, &out.PermanentError{Err: perr}, 0, result)
		}
	}

	// The network call happens with no database transaction open — never
	// hold a SQLite transaction across a slow, blocking external call.
	start := p.clock.Now()
	var messageID string
	var publishErr error
	if payloadAware {
		messageID, publishErr = payloadPublisher.PublishPayload(ctx, a, payload)
	} else {
		messageID, publishErr = publisher.Publish(ctx, a)
	}
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
	// DestinationEventPrefix prefixes the audit events of non-Telegram
	// deliveries ("publish_destination_delivered", ...), so Telegram counters
	// built on "publish"/"publish_failed"/"outbox_expired" stay unchanged.
	DestinationEventPrefix = "publish_destination_"
)

// expireItem closes the pre-network freshness race in one short transaction.
// It records no Attempt because the item never reached Telegram.
func (p *PublishOutbox) expireItem(ctx context.Context, runID string, item out.OutboxItem, a article.Article, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	if item.Destination.Normalized() != publishing.DestinationTelegram {
		return p.expireSecondaryItem(ctx, runID, item, a, now, result)
	}
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
	p.logger.Info("outbox item expired before publish", "run_id", runID, "article_id", a.ID, "outbox_id", item.ID, "destination", item.Destination.Normalized(), "lane", item.Lane.Normalized(), "reason", PublishExpiredReasonCode)
	result.Expired++
	return nil
}

// expireSecondaryItem expires a non-Telegram row: only that row dies. The
// article, its cluster gate and its cooldown belong to the Telegram delivery.
func (p *PublishOutbox) expireSecondaryItem(ctx context.Context, runID string, item out.OutboxItem, a article.Article, now time.Time, result *in.PublishOutboxResult) error {
	destination := item.Destination.Normalized()
	err := p.uow.Do(ctx, func(txCtx context.Context) error {
		if err := p.outbox.ExpireLeased(txCtx, item.ID, PublishExpiredReasonCode, now); err != nil {
			return fmt.Errorf("expire outbox item: %w", err)
		}
		return p.decisions.Append(txCtx, out.Event{
			ArticleID: a.ID, OccurredAt: now, FromStatus: a.Status, ToStatus: a.Status,
			EventType: DestinationEventPrefix + "expired", ReasonCode: PublishExpiredReasonCode,
			ReasonDetail: fmt.Sprintf("destination=%s outbox_id=%d", destination, item.ID),
		})
	})
	if err != nil {
		return fmt.Errorf("publish outbox: persist %s expiration for %s: %w", destination, a.ID, err)
	}
	p.logger.Info("outbox item expired before publish", "run_id", runID, "article_id", a.ID, "outbox_id", item.ID, "destination", destination, "destination", item.Destination.Normalized(), "lane", item.Lane.Normalized(), "reason", PublishExpiredReasonCode)
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
		if err := p.tracked(result, item.Destination, func() error { return p.expireItem(ctx, runID, item, a, result) }); err != nil {
			return err
		}
	}
	return nil
}

// evaluatePolicy gathers the counts publishing.Evaluate needs and asks it
// whether a may publish now.
func (p *PublishOutbox) evaluatePolicy(ctx context.Context, a article.Article, destination publishing.Destination, lane publishing.PublicationLane, now time.Time) (publishing.PolicyDecision, error) {
	lane = lane.Normalized()
	policy := p.policy
	if lane == publishing.LaneLive && p.policy.Live != nil {
		policy = *p.policy.Live
	}
	var burstCount int
	var err error
	destCounts, hasDest := p.outbox.(out.DestinationCountRepository)
	if !hasDest && destination != publishing.DestinationTelegram {
		return publishing.PolicyDecision{}, fmt.Errorf("outbox repository cannot count sends per destination %q", destination)
	}
	if hasDest {
		burstCount, err = destCounts.CountSucceededSinceDestination(ctx, destination, lane, now.Add(-policy.BurstWindow))
	} else if laneCounts, ok := p.outbox.(out.LaneCountRepository); ok {
		burstCount, err = laneCounts.CountSucceededSinceLane(ctx, lane, now.Add(-policy.BurstWindow))
	} else {
		burstCount, err = p.outbox.CountSucceededSince(ctx, now.Add(-policy.BurstWindow))
	}
	if err != nil {
		return publishing.PolicyDecision{}, fmt.Errorf("count succeeded since: %w", err)
	}

	var topicCount int
	if policy.SameTopicApplies(a.PrimaryEntity) {
		if hasDest {
			topicCount, err = destCounts.CountSucceededByPrimaryEntitySinceDestination(ctx, destination, lane, a.PrimaryEntity, now.Add(-policy.SameTopicWindow))
		} else if laneCounts, ok := p.outbox.(out.LaneCountRepository); ok {
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
	eventType := "publish_deferred"
	if item.Destination.Normalized() != publishing.DestinationTelegram {
		eventType = DestinationEventPrefix + "deferred"
	}
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: a.Status, ToStatus: a.Status,
		EventType: eventType, ReasonCode: decision.Reason,
		ReasonDetail: fmt.Sprintf("deferred until %s: %s", decision.RetryAt.Format(time.RFC3339), decision.Reason),
	}); err != nil {
		return fmt.Errorf("publish outbox: append deferred event for %s: %w", a.ID, err)
	}
	p.logger.Info("publish deferred", "run_id", runID, "article_id", a.ID, "destination", item.Destination.Normalized(), "lane", item.Lane.Normalized(), "reason", decision.Reason, "retry_at", decision.RetryAt)
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
	primary := item.Destination.Normalized() == publishing.DestinationTelegram
	if primary {
		a.Status = article.StatusPublished
		a.StatusReason = ""
	}

	err := p.uow.Do(ctx, func(txCtx context.Context) error {
		if primary {
			if err := p.articles.Save(txCtx, a); err != nil {
				return fmt.Errorf("save article: %w", err)
			}
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
		if !primary {
			return p.decisions.Append(txCtx, out.Event{
				ArticleID: a.ID, OccurredAt: now, FromStatus: a.Status, ToStatus: a.Status,
				EventType: DestinationEventPrefix + "delivered", ReasonDetail: fmt.Sprintf("destination=%s outbox_id=%d", item.Destination.Normalized(), item.ID),
			})
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
	p.logger.Info("article published", "run_id", runID, "article_id", a.ID, "cluster_id", a.ClusterID, "destination", item.Destination.Normalized(),
		"source", a.Source.ID, "destination", item.Destination.Normalized(), "lane", item.Lane.Normalized(), "latency_ms", duration.Milliseconds(), "retry_count", item.AttemptCount)
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
		"run_id", runID, "article_id", a.ID, "outbox_id", item.ID, "destination", item.Destination.Normalized(), "lane", item.Lane.Normalized(), "reason", reason)
	result.AmbiguousDetected++
	return nil
}

func (p *PublishOutbox) recordFailure(ctx context.Context, runID string, item out.OutboxItem, a article.Article, publishErr error, duration time.Duration, result *in.PublishOutboxResult) error {
	now := p.clock.Now()
	var permErr *out.PermanentError
	permanent := errors.As(publishErr, &permErr)
	nextAttempt := item.AttemptCount + 1
	dead := permanent || p.backoff.Exhausted(nextAttempt)

	primary := item.Destination.Normalized() == publishing.DestinationTelegram
	if primary {
		a.Status = article.StatusPendingRetry
		if dead {
			a.Status = article.StatusDiscarded
		}
		a.StatusReason = publishErr.Error()
	}

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
		if primary {
			if err := p.articles.Save(txCtx, a); err != nil {
				return fmt.Errorf("save article: %w", err)
			}
		}
		if err := p.outbox.MarkRetryable(txCtx, item.ID, publishErr.Error(), nextAvailableAt, dead, now); err != nil {
			return fmt.Errorf("mark retryable: %w", err)
		}
		if dead && primary && a.ClusterID != "" {
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
		if !primary {
			return p.decisions.Append(txCtx, out.Event{
				ArticleID: a.ID, OccurredAt: now, FromStatus: a.Status, ToStatus: a.Status,
				EventType:    DestinationEventPrefix + "failed",
				ReasonDetail: fmt.Sprintf("destination=%s outbox_id=%d: %s", item.Destination.Normalized(), item.ID, publishErr.Error()),
			})
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
		p.alertItem(ctx, "dead", item, publishErr.Error())
		p.logger.Warn("outbox item discarded", "run_id", runID, "article_id", a.ID, "destination", item.Destination.Normalized(), "lane", item.Lane.Normalized(), "permanent", permanent, "attempts", nextAttempt)
	} else {
		result.Retried++
		p.logger.Info("publish retry scheduled", "run_id", runID, "article_id", a.ID, "destination", item.Destination.Normalized(), "lane", item.Lane.Normalized(), "attempts", nextAttempt, "next_available_at", nextAvailableAt)
	}
	if rateLimited != nil && primary {
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
		if item.Destination.Normalized() == publishing.DestinationTelegram && item.SentMessageID == "" && p.channelImporter != nil && !historyFetched {
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

	if item.SentMessageID == "" && item.Destination.Normalized() != publishing.DestinationTelegram {
		// Channel history is the Telegram user session's. Another destination
		// is verified only through its publisher's own DestinationReconciler;
		// without one a blind resend is never allowed (ADR 0030).
		reconciler, ok := p.publishers[item.Destination.Normalized()].(out.DestinationReconciler)
		if !ok {
			return p.quarantineItem(ctx, item, fmt.Sprintf("reconciliation_unavailable: no reconciler for destination %s", item.Destination.Normalized()), result)
		}
		return p.reconcileWithDestination(ctx, reconciler, item, a, now, result)
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

// reconcileWithDestination resolves a sent_unconfirmed row with no recorded
// message ID through the destination publisher's own history lookup. The
// outcomes mirror the Telegram path: a match finalizes, a confident
// non-match schedules a retry, anything else quarantines.
func (p *PublishOutbox) reconcileWithDestination(ctx context.Context, reconciler out.DestinationReconciler, item out.OutboxItem, a article.Article, now time.Time, result *in.ReconcileResult) error {
	payload, err := publishing.ParseMessagePayload(item.Payload)
	if err != nil {
		return p.quarantineItem(ctx, item, "reconciliation_unavailable: unreadable payload", result)
	}
	messageID, matched, covered, err := reconciler.FindSent(ctx, out.SentLookup{
		Article: a, Payload: payload, SentAt: item.SentAt, ClockSkew: p.reconcile.ClockSkew,
	})
	if err != nil {
		p.logger.Warn("destination history reconciliation failed", "outbox_id", item.ID, "article_id", a.ID,
			"destination", item.Destination.Normalized(), "error", err.Error())
		return p.quarantineItem(ctx, item, "destination_history_lookup_failed", result)
	}
	switch {
	case matched:
		if err := p.outbox.RecordSentUnconfirmed(ctx, item.ID, messageID, "reconciled_from_destination_history", now); err != nil {
			return fmt.Errorf("reconcile outbox: record matched message id for %s: %w", a.ID, err)
		}
		if err := p.finalizeDelivered(ctx, "reconcile", item, a, messageID, 0, &in.PublishOutboxResult{}); err != nil {
			return err
		}
		p.logger.Info("ambiguous delivery reconciled positive (destination history)", "outbox_id", item.ID, "article_id", a.ID)
		result.ReconciledPositive++
		return nil
	case covered:
		return p.reconcileNegative(ctx, item, a, now, result)
	default:
		return p.quarantineItem(ctx, item, "destination_history_inconclusive", result)
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
		"outbox_id", item.ID, "article_id", item.ArticleID, "destination", item.Destination.Normalized(), "reason", reason)
	result.Quarantined++
	p.alertItem(ctx, "quarantined", item, reason)
	return nil
}
