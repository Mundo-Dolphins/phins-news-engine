package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// terminalRejectedStatuses are the statuses eligible for rejected_articles_days
// retention. article.StatusPublished and article.StatusRejectedDuplicate are
// deliberately excluded: Published is never deleted, and Duplicate has its
// own, usually-shorter, duplicates_days window.
var terminalRejectedStatuses = []article.Status{
	article.StatusRejectedValidation,
	article.StatusRejectedNotRelated,
	article.StatusRejectedQuality,
	article.StatusRejectedStale,
	article.StatusDiscarded,
}

// RunRetention deletes rejected/duplicate articles, old audit events, old
// publication attempts, and closed clusters older than their configured
// windows. It never deletes published articles or promoted
// model/dictionary registry rows — see ADR 0005. Purging an article does
// cascade-delete any human_labels row for it (human_labels is an
// operational review journal, not the durable corpus — see ADR 0005),
// but that's expected: the durable training example, if any, already
// lives independently in editorial_examples, which has no foreign key to
// articles and is unreachable by anything this use case does.
type RunRetention struct {
	articles     out.ArticleRepository
	decisions    out.DecisionRepository
	outbox       out.OutboxRepository
	clusters     out.ClusterRepository
	labels       out.HumanLabelRepository
	checkpointer out.Checkpointer
	windows      in.RetentionWindows
	clock        out.Clock
	logger       out.Logger

	media          out.MediaSweeper
	openPayloads   out.OpenPayloadRepository
	mediaRetention time.Duration
}

// NewRunRetention builds a RunRetention use case.
func NewRunRetention(
	articles out.ArticleRepository,
	decisions out.DecisionRepository,
	outbox out.OutboxRepository,
	clusters out.ClusterRepository,
	labels out.HumanLabelRepository,
	checkpointer out.Checkpointer,
	windows in.RetentionWindows,
	clock out.Clock,
	logger out.Logger,
) *RunRetention {
	return &RunRetention{
		articles:     articles,
		decisions:    decisions,
		outbox:       outbox,
		clusters:     clusters,
		labels:       labels,
		checkpointer: checkpointer,
		windows:      windows,
		clock:        clock,
		logger:       logger,
	}
}

// SetMediaRetention enables the media-store step (ADR 0036): files older than
// retention are deleted, except those referenced by an outbox row that is
// still pending, leased, retryable or sent_unconfirmed. Without it, or with a
// non-positive retention, the step is skipped. The caller is responsible for
// bounding retention (config.MediaConfig does).
func (r *RunRetention) SetMediaRetention(media out.MediaSweeper, payloads out.OpenPayloadRepository, retention time.Duration) {
	r.media, r.openPayloads, r.mediaRetention = media, payloads, retention
}

// sweepMedia deletes expired media files. It lists the open outbox payloads
// first and does nothing if that fails: without the list, no file is known to
// be unreferenced.
func (r *RunRetention) sweepMedia(ctx context.Context, now time.Time, result *in.RetentionResult) error {
	raw, err := r.openPayloads.ListOpenPayloads(ctx)
	if err != nil {
		return fmt.Errorf("list open payloads: %w", err)
	}
	keep := make(map[string]struct{})
	for _, p := range raw {
		payload, err := publishing.ParseMessagePayload(p)
		if err != nil {
			// An undecodable payload cannot be read by a publisher either, but
			// refuse to guess which files it meant.
			return fmt.Errorf("open payload: %w", err)
		}
		for _, img := range payload.Images {
			keep[img.Path] = struct{}{}
		}
	}
	res, err := r.media.DeleteOlderThan(ctx, now.Add(-r.mediaRetention), keep)
	result.MediaDeleted, result.MediaBytesDeleted, result.MediaProtected = res.Deleted, res.BytesDeleted, res.Protected
	return err
}

// Run implements in.RunRetention.
func (r *RunRetention) Run(ctx context.Context) (in.RetentionResult, error) {
	var result in.RetentionResult
	now := r.clock.Now()

	attemptsDeleted, err := r.outbox.DeleteOldAttempts(ctx, now.Add(-r.windows.PublicationAttempts))
	if err != nil {
		return result, fmt.Errorf("run retention: publication attempts: %w", err)
	}
	result.AttemptsDeleted = attemptsDeleted

	clustersDeleted, err := r.clusters.DeleteClosedOlderThan(ctx, now.Add(-r.windows.Clusters))
	if err != nil {
		return result, fmt.Errorf("run retention: clusters: %w", err)
	}
	result.ClustersDeleted = clustersDeleted

	eventsDeleted, err := r.decisions.DeleteOlderThan(ctx, now.Add(-r.windows.AuditEvents))
	if err != nil {
		return result, fmt.Errorf("run retention: audit events: %w", err)
	}
	result.AuditEventsDeleted = eventsDeleted

	duplicatesDeleted, err := r.articles.DeleteByStatusOlderThan(ctx, []article.Status{article.StatusRejectedDuplicate}, now.Add(-r.windows.Duplicates))
	if err != nil {
		return result, fmt.Errorf("run retention: duplicates: %w", err)
	}
	result.DuplicatesDeleted = duplicatesDeleted

	rejectedDeleted, err := r.articles.DeleteByStatusOlderThan(ctx, terminalRejectedStatuses, now.Add(-r.windows.RejectedArticles))
	if err != nil {
		return result, fmt.Errorf("run retention: rejected articles: %w", err)
	}
	result.RejectedArticlesDeleted = rejectedDeleted

	if r.media != nil && r.openPayloads != nil && r.mediaRetention > 0 {
		if err := r.sweepMedia(ctx, now, &result); err != nil {
			return result, fmt.Errorf("run retention: media: %w", err)
		}
	}

	editorialExamplesRetained, err := r.labels.CountEditorialExamples(ctx)
	if err != nil {
		r.logger.Warn("count editorial examples failed", "error", err.Error())
	} else {
		result.EditorialExamplesRetained = editorialExamplesRetained
	}

	if err := r.checkpointer.Checkpoint(ctx); err != nil {
		r.logger.Warn("wal checkpoint failed", "error", err.Error())
	}

	r.logger.Info(
		"retention completed",
		"rejected_articles_deleted", result.RejectedArticlesDeleted,
		"duplicates_deleted", result.DuplicatesDeleted,
		"audit_events_deleted", result.AuditEventsDeleted,
		"attempts_deleted", result.AttemptsDeleted,
		"clusters_deleted", result.ClustersDeleted,
		"media_deleted", result.MediaDeleted,
		"media_protected", result.MediaProtected,
		"editorial_examples_retained", result.EditorialExamplesRetained,
	)
	return result, nil
}
