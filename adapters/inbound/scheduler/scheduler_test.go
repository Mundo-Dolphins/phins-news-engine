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
