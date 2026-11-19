package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/inbound/scheduler"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

type countingProcessCycle struct {
	calls atomic.Int32
	err   error
}

func (c *countingProcessCycle) Run(context.Context) (in.ProcessCycleResult, error) {
	c.calls.Add(1)
	return in.ProcessCycleResult{}, c.err
}

type countingPublishOutbox struct {
	calls atomic.Int32
	err   error
}

func (c *countingPublishOutbox) Run(context.Context) (in.PublishOutboxResult, error) {
	c.calls.Add(1)
	return in.PublishOutboxResult{}, c.err
}

type countingRunRetention struct {
	calls atomic.Int32
	err   error
}

func (c *countingRunRetention) Run(context.Context) (in.RetentionResult, error) {
	c.calls.Add(1)
	return in.RetentionResult{}, c.err
}

type countingCloseInactiveClusters struct {
	calls atomic.Int32
	err   error
}

func (c *countingCloseInactiveClusters) Run(context.Context) (in.CloseInactiveClustersResult, error) {
	c.calls.Add(1)
	return in.CloseInactiveClustersResult{}, c.err
}

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)        {}
func (nopLogger) Info(string, ...any)         {}
func (nopLogger) Warn(string, ...any)         {}
func (nopLogger) Error(string, error, ...any) {}

func TestScheduler_RunsCycleImmediatelyAndOnTicks(t *testing.T) {
	cycle := &countingProcessCycle{}
	s := scheduler.New(cycle, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, &countingRunRetention{}, 5*time.Millisecond, time.Hour, time.Hour, nopLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := s.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context.DeadlineExceeded", err)
	}
	if cycle.calls.Load() < 2 {
		t.Errorf("cycle calls = %d, want at least 2 (immediate + at least one tick)", cycle.calls.Load())
	}
}

func TestScheduler_RunsPublishOutboxOnItsOwnTicker(t *testing.T) {
	cycle := &countingProcessCycle{}
	outbox := &countingPublishOutbox{}
	s := scheduler.New(cycle, outbox, &countingCloseInactiveClusters{}, &countingRunRetention{}, time.Hour, 5*time.Millisecond, time.Hour, nopLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	if outbox.calls.Load() < 1 {
		t.Errorf("outbox calls = %d, want at least 1", outbox.calls.Load())
	}
	if cycle.calls.Load() != 1 {
		t.Errorf("cycle calls = %d, want exactly 1 (only the immediate run)", cycle.calls.Load())
	}
}

func TestScheduler_RunsRetentionOnItsOwnTicker(t *testing.T) {
	cycle := &countingProcessCycle{}
	retention := &countingRunRetention{}
	s := scheduler.New(cycle, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, retention, time.Hour, time.Hour, 5*time.Millisecond, nopLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	if retention.calls.Load() < 1 {
		t.Errorf("retention calls = %d, want at least 1", retention.calls.Load())
	}
	if cycle.calls.Load() != 1 {
		t.Errorf("cycle calls = %d, want exactly 1 (only the immediate run)", cycle.calls.Load())
	}
}

func TestScheduler_ContinuesAfterCycleError(t *testing.T) {
	cycle := &countingProcessCycle{err: errors.New("fetch failed")}
	s := scheduler.New(cycle, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, &countingRunRetention{}, 5*time.Millisecond, time.Hour, time.Hour, nopLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	if cycle.calls.Load() < 2 {
		t.Errorf("cycle calls = %d, want scheduler to keep ticking despite errors", cycle.calls.Load())
	}
}

type blockingProcessCycle struct {
	calls   atomic.Int32
	release chan struct{}
}

func (c *blockingProcessCycle) Run(ctx context.Context) (in.ProcessCycleResult, error) {
	c.calls.Add(1)
	select {
	case <-c.release:
	case <-ctx.Done():
	}
	return in.ProcessCycleResult{}, nil
}

func TestScheduler_SlowProcessCycleDoesNotBlockPublishOutbox(t *testing.T) {
	cycle := &blockingProcessCycle{release: make(chan struct{})}
	defer close(cycle.release)
	outbox := &countingPublishOutbox{}
	s := scheduler.New(cycle, outbox, &countingCloseInactiveClusters{}, &countingRunRetention{}, time.Hour, 5*time.Millisecond, time.Hour, nopLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	if outbox.calls.Load() < 2 {
		t.Errorf("outbox calls = %d, want at least 2 — the immediate blocking ProcessCycle run must not stall PublishOutbox's own ticker", outbox.calls.Load())
	}
}

func TestScheduler_ClosesInactiveClustersBeforeRetentionOnCleanupTick(t *testing.T) {
	var mu sync.Mutex
	var order []string
	closeClusters := &orderedCloseInactiveClusters{order: &order, mu: &mu}
	retention := &orderedRunRetention{order: &order, mu: &mu}
	s := scheduler.New(&countingProcessCycle{}, &countingPublishOutbox{}, closeClusters, retention, time.Hour, time.Hour, 5*time.Millisecond, nopLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "close" || order[1] != "retention" {
		t.Fatalf("call order = %v, want [close retention ...]", order)
	}
}

type orderedCloseInactiveClusters struct {
	order *[]string
	mu    *sync.Mutex
}

func (c *orderedCloseInactiveClusters) Run(context.Context) (in.CloseInactiveClustersResult, error) {
	c.mu.Lock()
	*c.order = append(*c.order, "close")
	c.mu.Unlock()
	return in.CloseInactiveClustersResult{}, nil
}

type orderedRunRetention struct {
	order *[]string
	mu    *sync.Mutex
}

func (r *orderedRunRetention) Run(context.Context) (in.RetentionResult, error) {
	r.mu.Lock()
	*r.order = append(*r.order, "retention")
	r.mu.Unlock()
	return in.RetentionResult{}, nil
}

type countingTimedEvents struct {
	calls atomic.Int32
}

func (c *countingTimedEvents) Run(context.Context) (in.RunTimedEventsResult, error) {
	c.calls.Add(1)
	return in.RunTimedEventsResult{}, nil
}

func TestScheduler_RunsTimedEventsWhenSet(t *testing.T) {
	timed := &countingTimedEvents{}
	s := scheduler.New(&countingProcessCycle{}, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, &countingRunRetention{}, time.Hour, time.Hour, time.Hour, nopLogger{})
	s.SetTimedEvents(timed, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if got := timed.calls.Load(); got < 2 {
		t.Fatalf("timed events ran %d times, want an immediate run plus ticks", got)
	}
}

type countingTasks struct {
	calls atomic.Int32
}

func (c *countingTasks) Run(context.Context) (in.RunTasksResult, error) {
	c.calls.Add(1)
	return in.RunTasksResult{}, nil
}

func TestScheduler_RunsTaskLoopOnlyWhenSet(t *testing.T) {
	tasks := &countingTasks{}
	s := scheduler.New(&countingProcessCycle{}, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, &countingRunRetention{}, time.Hour, time.Hour, time.Hour, nopLogger{})
	s.SetTasks(tasks, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if got := tasks.calls.Load(); got < 2 {
		t.Fatalf("tasks ran %d times, want an immediate run plus ticks", got)
	}
}

func TestScheduler_WakeTasksRunsTheTaskLoopBeforeItsTick(t *testing.T) {
	tasks := &countingTasks{}
	s := scheduler.New(&countingProcessCycle{}, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, &countingRunRetention{}, time.Hour, time.Hour, time.Hour, nopLogger{})
	s.SetTasks(tasks, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	waitFor(t, func() bool { return tasks.calls.Load() == 1 }) // the immediate run
	s.WakeTasks()
	waitFor(t, func() bool { return tasks.calls.Load() == 2 })
	// Wakes never block, even when many arrive at once.
	for i := 0; i < 10; i++ {
		s.WakeTasks()
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestScheduler_WakeTasksWithoutTaskLoopDoesNotBlock(t *testing.T) {
	s := scheduler.New(&countingProcessCycle{}, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, &countingRunRetention{}, time.Hour, time.Hour, time.Hour, nopLogger{})
	s.WakeTasks()
	s.WakeTasks()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(time.Millisecond)
	}
}

// signallingPoller stands in for the update poller: once the task loop has
// had its immediate run it "saves a signal" and wakes the loop, as the
// engine wires it (Config.Wake = WakeTasks).
type signallingPoller struct {
	tasks *countingTasks
	wake  func()
}

func (p *signallingPoller) Run(ctx context.Context) error {
	for p.tasks.calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	p.wake()
	<-ctx.Done()
	return ctx.Err()
}

func TestScheduler_SignalFromPollerRunsTasksWithoutWaitingForTick(t *testing.T) {
	tasks := &countingTasks{}
	s := scheduler.New(&countingProcessCycle{}, &countingPublishOutbox{}, &countingCloseInactiveClusters{}, &countingRunRetention{}, time.Hour, time.Hour, time.Hour, nopLogger{})
	s.SetTasks(tasks, time.Hour)
	s.SetUpdatePoller(&signallingPoller{tasks: tasks, wake: s.WakeTasks})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	waitFor(t, func() bool { return tasks.calls.Load() == 2 })
	cancel()
	<-done
}
