// Package scheduler is the driving adapter that periodically triggers the
// application's ProcessCycle, PublishOutbox, CloseInactiveClusters, and
// RunRetention use cases, plus the timed event handlers when configured.
package scheduler

import (
	"context"
	"sync"
	"time"

	appin "github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Scheduler runs ProcessCycle on cycleInterval, PublishOutbox on
// outboxInterval, and CloseInactiveClusters immediately followed by
// RunRetention — in that order, every tick — on cleanupInterval, until ctx
// is cancelled. Closing before retention on the same tick is what
// satisfies "retention only purges clusters closed safely"; there is no other
// coupling between the two use cases' Go types.
type Scheduler struct {
	processCycle          appin.ProcessCycle
	publishOutbox         appin.PublishOutbox
	closeInactiveClusters appin.CloseInactiveClusters
	runRetention          appin.RunRetention
	cycleInterval         time.Duration
	outboxInterval        time.Duration
	cleanupInterval       time.Duration
	logger                appout.Logger
	timedEvents           appin.RunTimedEvents
	timedEventInterval    time.Duration
}

// New builds a Scheduler.
func New(
	processCycle appin.ProcessCycle,
	publishOutbox appin.PublishOutbox,
	closeInactiveClusters appin.CloseInactiveClusters,
	runRetention appin.RunRetention,
	cycleInterval, outboxInterval, cleanupInterval time.Duration,
	logger appout.Logger,
) *Scheduler {
	return &Scheduler{
		processCycle:          processCycle,
		publishOutbox:         publishOutbox,
		closeInactiveClusters: closeInactiveClusters,
		runRetention:          runRetention,
		cycleInterval:         cycleInterval,
		outboxInterval:        outboxInterval,
		cleanupInterval:       cleanupInterval,
		logger:                logger,
	}
}

// SetTimedEvents adds a fourth loop that runs the timed event handlers
// (ADR 0038) every interval. Without it the scheduler behaves as before.
// Call it before Run.
func (s *Scheduler) SetTimedEvents(r appin.RunTimedEvents, interval time.Duration) {
	s.timedEvents = r
	s.timedEventInterval = interval
}

// Run starts one goroutine per interval — ProcessCycle, PublishOutbox, and
// the cleanup tick (CloseInactiveClusters then RunRetention, sequentially,
// every tick) — each running immediately and then on its own ticker, until
// ctx is cancelled, at which point Run waits for all three to finish their
// current iteration before returning.
func (s *Scheduler) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		s.runLoop(ctx, s.cycleInterval, func(ctx context.Context) {
			if _, err := s.processCycle.Run(ctx); err != nil {
				s.logger.Error("process cycle failed", err)
			}
		})
	}()
	go func() {
		defer wg.Done()
		s.runLoop(ctx, s.outboxInterval, func(ctx context.Context) {
			if _, err := s.publishOutbox.Run(ctx); err != nil {
				s.logger.Error("publish outbox failed", err)
			}
		})
	}()
	go func() {
		defer wg.Done()
		s.runLoop(ctx, s.cleanupInterval, func(ctx context.Context) {
			if _, err := s.closeInactiveClusters.Run(ctx); err != nil {
				s.logger.Error("close inactive clusters failed", err)
			}
			if _, err := s.runRetention.Run(ctx); err != nil {
				s.logger.Error("run retention failed", err)
			}
		})
	}()

	if s.timedEvents != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runLoop(ctx, s.timedEventInterval, func(ctx context.Context) {
				if _, err := s.timedEvents.Run(ctx); err != nil && ctx.Err() == nil {
					s.logger.Error("run timed events failed", err)
				}
			})
		}()
	}

	wg.Wait()
	return ctx.Err()
}

// runLoop calls fn immediately, then again on every tick of interval,
// until ctx is done.
func (s *Scheduler) runLoop(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	fn(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}
