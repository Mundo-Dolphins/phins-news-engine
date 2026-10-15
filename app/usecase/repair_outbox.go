// Package usecase (this file): RepairOutbox applies an operator's manual,
// audited decision to an outbox item Reconcile could not resolve
// deterministically — the last-resort path ADR 0030 requires so an
// unresolvable ambiguity is never left stuck forever, and is never
// resolved silently.
package usecase

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// RepairOutbox applies a human operator's decision to a stuck outbox
// item.
type RepairOutbox struct {
	outbox              out.OutboxRepository
	articles            out.ArticleRepository
	clusterPublications out.ClusterPublicationRepository
	decisions           out.DecisionRepository
	uow                 out.UnitOfWork
	clock               out.Clock
	logger              out.Logger
}

// NewRepairOutbox builds a RepairOutbox use case.
func NewRepairOutbox(
	outbox out.OutboxRepository,
	articles out.ArticleRepository,
	clusterPublications out.ClusterPublicationRepository,
	decisions out.DecisionRepository,
	uow out.UnitOfWork,
	clock out.Clock,
	logger out.Logger,
) *RepairOutbox {
	return &RepairOutbox{
		outbox:              outbox,
		articles:            articles,
		clusterPublications: clusterPublications,
		decisions:           decisions,
		uow:                 uow,
		clock:               clock,
		logger:              logger,
	}
}

// Run applies action to outboxID, requiring a non-empty operatorReason —
// every manual repair must leave an auditable "why" alongside the "who
// decided this couldn't be resolved automatically." articleID is the
// article the outbox item belongs to, for the decision event; the CLI
// caller looks this up before calling Run.
//
// action and operatorReason are validated before either the repository or
// the decision log is touched, so an invalid call has no side effects.
// The repair, the article's own terminal status, the event-cluster
// publication gate and the audit event are all written inside a
// single uow.Do transaction: either all persist or none do.
//
// A terminal repair must also resolve the cluster publication gate,
// otherwise the article's event_clusters row stays 'reserved' forever and
// no other article for that event can ever publish (Reserve only succeeds
// on a 'released' row):
//   - deliver  -> Confirm: this article really did reach the channel, so it
//     is the cluster's initial publication.
//   - discard  -> Release: it never will, so free the cluster for a future
//     article.
//   - retry    -> neither: the item re-enters the normal publish flow,
//     which will Confirm or Release it through the existing paths, exactly
//     like recordFailure's non-dead path leaves the gate alone.
func (r *RepairOutbox) Run(ctx context.Context, outboxID int64, action publishing.RepairAction, articleID, operatorReason string) error {
	if !publishing.ValidRepairAction(action) {
		return fmt.Errorf("repair outbox %d: unrecognized action %q", outboxID, action)
	}
	if operatorReason == "" {
		return fmt.Errorf("repair outbox %d: an operator reason is required", outboxID)
	}

	// Fetched once, before any write: every action needs a.ClusterID, and
	// deliver/discard also mutate a.Status. Fail fast, like validation
	// above, so a repair that cannot be completed has no side effects.
	a, err := r.articles.FindByID(ctx, articleID)
	if err != nil {
		return fmt.Errorf("repair outbox %d: find article %s: %w", outboxID, articleID, err)
	}

	now := r.clock.Now()
	// out.Event carries FromStatus/ToStatus for article status transitions.
	// deliver/discard really do move the article to a terminal status, so
	// they record it; retry leaves the article alone (the next publish
	// attempt decides), so both stay the zero article.Status there.
	var fromStatus, toStatus article.Status
	err = r.uow.Do(ctx, func(txCtx context.Context) error {
		if err := r.outbox.Repair(txCtx, outboxID, articleID, action, operatorReason, now); err != nil {
			return err
		}
		switch action {
		case publishing.RepairDeliver:
			a.Status = article.StatusPublished
			a.StatusReason = ""
			if err := r.articles.Save(txCtx, a); err != nil {
				return fmt.Errorf("save article: %w", err)
			}
			if a.ClusterID != "" {
				if err := r.clusterPublications.Confirm(txCtx, a.ClusterID, a.ID, outboxID, now); err != nil {
					return fmt.Errorf("confirm cluster publication gate: %w", err)
				}
			}
			fromStatus, toStatus = article.StatusPublishing, article.StatusPublished
		case publishing.RepairDiscard:
			a.Status = article.StatusDiscarded
			a.StatusReason = operatorReason
			if err := r.articles.Save(txCtx, a); err != nil {
				return fmt.Errorf("save article: %w", err)
			}
			if a.ClusterID != "" {
				if err := r.clusterPublications.Release(txCtx, a.ClusterID, a.ID, now); err != nil {
					return fmt.Errorf("release cluster publication gate: %w", err)
				}
			}
			fromStatus, toStatus = article.StatusPublishing, article.StatusDiscarded
		case publishing.RepairRetry:
			// The item goes back into the normal retry flow: the cluster
			// gate deliberately stays 'reserved' (recordFailure's non-dead
			// path does the same) so no second article can claim the slot
			// while this one is still live. The article's own status is
			// left to the next publish attempt.
		}
		return r.decisions.Append(txCtx, out.Event{
			ArticleID: articleID, OccurredAt: now, FromStatus: fromStatus, ToStatus: toStatus,
			EventType: "outbox_manual_repair", ReasonCode: string(action), ReasonDetail: operatorReason,
		})
	})
	if err != nil {
		return fmt.Errorf("repair outbox %d: %w", outboxID, err)
	}
	r.logger.Warn("outbox item manually repaired", "outbox_id", outboxID, "article_id", articleID,
		"cluster_id", a.ClusterID, "action", action)
	return nil
}
