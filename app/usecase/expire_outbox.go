package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// OutboxExpiredReasonCode is the stable audit/status code for an operator-approved expiration.
	OutboxExpiredReasonCode = "outbox_expired_operator"
	// OutboxExpiredEventType identifies the corresponding processing event.
	OutboxExpiredEventType = "outbox_expired"
)

// ExpireOutboxRequest defines the safe candidate-selection filters.
type ExpireOutboxRequest struct {
	OlderThan   time.Duration
	ContentType article.ContentType
	SourceID    string
	Limit       int
}

// ExpireOutboxPreview is the immutable batch shown to the operator.
type ExpireOutboxPreview struct {
	Candidates []out.OutboxExpirationCandidate
	Cutoff     time.Time
}

// ExpireOutbox audits and atomically expires an operator-approved batch.
type ExpireOutbox struct {
	expirations         out.OutboxExpirationRepository
	articles            out.ArticleRepository
	clusterPublications out.ClusterPublicationRepository
	cooldowns           out.SourcePolicyCooldownRepository
	decisions           out.DecisionRepository
	uow                 out.UnitOfWork
	clock               out.Clock
}

// NewExpireOutbox constructs the expiration use case from driven ports.
func NewExpireOutbox(expirations out.OutboxExpirationRepository, articles out.ArticleRepository, clusterPublications out.ClusterPublicationRepository, cooldowns out.SourcePolicyCooldownRepository, decisions out.DecisionRepository, uow out.UnitOfWork, clock out.Clock) *ExpireOutbox {
	return &ExpireOutbox{expirations: expirations, articles: articles, clusterPublications: clusterPublications, cooldowns: cooldowns, decisions: decisions, uow: uow, clock: clock}
}

// Preview lists candidates without writing any state.
func (e *ExpireOutbox) Preview(ctx context.Context, req ExpireOutboxRequest) (ExpireOutboxPreview, error) {
	if req.OlderThan <= 0 {
		return ExpireOutboxPreview{}, fmt.Errorf("expire outbox: older-than must be positive")
	}
	if req.Limit <= 0 {
		return ExpireOutboxPreview{}, fmt.Errorf("expire outbox: limit must be positive")
	}
	if req.ContentType != "" && req.ContentType != article.ContentTypeArticle && req.ContentType != article.ContentTypeSocialPost && req.ContentType != article.ContentTypeVideo && req.ContentType != article.ContentTypeUnknown {
		return ExpireOutboxPreview{}, fmt.Errorf("expire outbox: invalid content type %q", req.ContentType)
	}
	sourceID := strings.TrimSpace(req.SourceID)
	if sourceID != "" {
		if _, err := source.ParseID(sourceID); err != nil {
			return ExpireOutboxPreview{}, fmt.Errorf("expire outbox: invalid source id: %w", err)
		}
	}
	now := e.clock.Now()
	cutoff := now.Add(-req.OlderThan)
	candidates, err := e.expirations.ListExpirationCandidates(ctx, out.OutboxExpirationFilter{PublishedBefore: cutoff, ExpiredBefore: now, ContentType: req.ContentType, SourceID: sourceID, Limit: req.Limit})
	if err != nil {
		return ExpireOutboxPreview{}, fmt.Errorf("expire outbox: list candidates: %w", err)
	}
	return ExpireOutboxPreview{Candidates: candidates, Cutoff: cutoff}, nil
}

// Apply expires exactly the displayed candidates in one transaction.
func (e *ExpireOutbox) Apply(ctx context.Context, candidates []out.OutboxExpirationCandidate, operatorReason string) (int, error) {
	operatorReason = strings.TrimSpace(operatorReason)
	if operatorReason == "" {
		return 0, fmt.Errorf("expire outbox: an operator reason is required")
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	now := e.clock.Now()
	detail, err := json.Marshal(map[string]string{"operator_reason": operatorReason})
	if err != nil {
		return 0, fmt.Errorf("expire outbox: encode audit detail: %w", err)
	}
	err = e.uow.Do(ctx, func(txCtx context.Context) error {
		for _, candidate := range candidates {
			a, err := e.articles.FindByID(txCtx, candidate.ArticleID)
			if err != nil {
				return fmt.Errorf("find article %s: %w", candidate.ArticleID, err)
			}
			if err := e.expirations.ExpireClaimable(txCtx, candidate.OutboxID, candidate.ArticleID, OutboxExpiredReasonCode+": "+operatorReason, now); err != nil {
				return err
			}
			fromStatus := a.Status
			a.Status = article.StatusDiscarded
			a.StatusReason = OutboxExpiredReasonCode + ": " + operatorReason
			if err := e.articles.Save(txCtx, a); err != nil {
				return fmt.Errorf("save article %s: %w", a.ID, err)
			}
			if a.ClusterID != "" {
				if err := e.clusterPublications.Release(txCtx, a.ClusterID, a.ID, now); err != nil {
					return fmt.Errorf("release cluster publication for %s: %w", a.ID, err)
				}
				if _, err := e.cooldowns.Release(txCtx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
					return fmt.Errorf("release source cooldown for %s: %w", a.ID, err)
				}
			}
			if err := e.decisions.Append(txCtx, out.Event{ArticleID: a.ID, OccurredAt: now, FromStatus: fromStatus, ToStatus: article.StatusDiscarded, EventType: OutboxExpiredEventType, ReasonCode: OutboxExpiredReasonCode, ReasonDetail: string(detail)}); err != nil {
				return fmt.Errorf("append expiration event for %s: %w", a.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("expire outbox: apply: %w", err)
	}
	return len(candidates), nil
}
