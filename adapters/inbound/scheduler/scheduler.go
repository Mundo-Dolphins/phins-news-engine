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
	tasks                 appin.RunTasks
	taskInterval          time.Duration
	taskWake              chan struct{}
	updatePoller          UpdatePoller
}

// UpdatePoller is a long-running loop (the Telegram update poller) that
// blocks until ctx is done.
type UpdatePoller interface {
	Run(ctx context.Context) error
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
		taskWake:              make(chan struct{}, 1),
	}
}

// SetTimedEvents adds a fourth loop that runs the timed event handlers
// (ADR 0038) every interval. Without it the scheduler behaves as before.
// Call it before Run.
func (s *Scheduler) SetTimedEvents(r appin.RunTimedEvents, interval time.Duration) {
	s.timedEvents = r
	s.timedEventInterval = interval
}

// SetTasks adds a loop that runs the background task runners every interval.
// Without it the scheduler behaves as before. Call it before Run.
func (s *Scheduler) SetTasks(r appin.RunTasks, interval time.Duration) {
	s.tasks = r
	s.taskInterval = interval
}

// WakeTasks asks the task loop to run now instead of at its next tick (for
// example after a signal arrived). It never blocks: wakes that arrive while
// one is already pending collapse into it. Without SetTasks it does nothing.
func (s *Scheduler) WakeTasks() {
	select {
	case s.taskWake <- struct{}{}:
	default:
	}
}

// SetUpdatePoller adds a goroutine that runs the given long-polling loop
// for the scheduler's lifetime. Without it the scheduler behaves as before.
// Call it before Run.
func (s *Scheduler) SetUpdatePoller(p UpdatePoller) { s.updatePoller = p }

// Run starts one goroutine per interval — ProcessCycle, PublishOutbox, and
// the cleanup tick (CloseInactiveClusters then RunRetention, sequentially,
// every tick), plus the timed event and task loops and the update poller
// when set — each loop running immediately and then on its own ticker (the
// task loop also on WakeTasks), until ctx is cancelled, at which point Run
// waits for every goroutine to finish its current iteration before
// returning.
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

	if s.tasks != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runLoopWake(ctx, s.taskInterval, s.taskWake, func(ctx context.Context) {
				if _, err := s.tasks.Run(ctx); err != nil && ctx.Err() == nil {
					s.logger.Error("run tasks failed", err)
				}
			})
		}()
	}

	if s.updatePoller != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.updatePoller.Run(ctx); err != nil && ctx.Err() == nil {
				s.logger.Error("telegram update poller stopped", err)
			}
		}()
	}

	wg.Wait()
	return ctx.Err()
}

// runLoop calls fn immediately, then again on every tick of interval,
// until ctx is done.
func (s *Scheduler) runLoop(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	s.runLoopWake(ctx, interval, nil, fn)
}

// runLoopWake is runLoop that also calls fn whenever wake receives (a nil
// wake never does).
func (s *Scheduler) runLoopWake(ctx context.Context, interval time.Duration, wake <-chan struct{}, fn func(context.Context)) {
	fn(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		case <-wake:
			fn(ctx)
		}
	}
}
