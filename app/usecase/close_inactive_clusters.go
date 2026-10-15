package usecase

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// CloseInactiveClusters implements in.CloseInactiveClusters. It is an
// idempotent, resumable maintenance job.
type CloseInactiveClusters struct {
	clusters out.ClusterRepository
	clock    out.Clock
	logger   out.Logger
	cfg      in.CloseInactiveClustersConfig
}

// NewCloseInactiveClusters builds a CloseInactiveClusters use case.
func NewCloseInactiveClusters(clusters out.ClusterRepository, clock out.Clock, logger out.Logger, cfg in.CloseInactiveClustersConfig) *CloseInactiveClusters {
	return &CloseInactiveClusters{clusters: clusters, clock: clock, logger: logger, cfg: cfg}
}

// Run implements in.CloseInactiveClusters. cutoff and deadline are both
// computed once, from a single clock.Now() read at the start — cutoff so
// every batch in this run uses the same eligibility line (rather than
// drifting as the loop progresses), deadline so TimeBudget is measured
// from the run's start, not accumulated per batch.
func (c *CloseInactiveClusters) Run(ctx context.Context) (in.CloseInactiveClustersResult, error) {
	var result in.CloseInactiveClustersResult
	start := c.clock.Now()
	deadline := start.Add(c.cfg.TimeBudget)
	cutoff := start.Add(-c.cfg.CloseAfter)

	for result.BatchesRun < c.cfg.MaxBatchesPerRun {
		if ctx.Err() != nil {
			break
		}
		now := c.clock.Now()
		if !now.Before(deadline) {
			break
		}
		closed, err := c.clusters.CloseInactiveBatch(ctx, cutoff, c.cfg.CloseReason, c.cfg.BatchSize, now)
		if err != nil {
			return result, fmt.Errorf("close inactive clusters: %w", err)
		}
		result.ClustersClosed += closed
		result.BatchesRun++
		if closed < c.cfg.BatchSize {
			break
		}
	}

	if stats, err := c.clusters.Stats(ctx, c.clock.Now()); err != nil {
		c.logger.Warn("cluster lifecycle stats failed", "error", err.Error())
	} else {
		c.logger.Info("cluster lifecycle stats",
			"open_count", stats.OpenCount,
			"closed_count", stats.ClosedCount,
			"open_age_p95_hours", stats.OpenAgeP95Seconds/3600,
			"open_age_max_hours", stats.OpenAgeMaxSeconds/3600,
			"open_member_count_max", stats.OpenMemberCountMax,
			"reopened_total", stats.ReopenedTotal,
		)
	}

	c.logger.Info("close inactive clusters completed", "clusters_closed", result.ClustersClosed, "batches_run", result.BatchesRun)
	return result, nil
}

var _ in.CloseInactiveClusters = (*CloseInactiveClusters)(nil)
