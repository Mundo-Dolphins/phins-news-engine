package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// mutableClock is a pointer-based, test-local Clock whose Now() can be
// advanced mid-test — unlike the package's shared fakeClock (fakes_test.go),
// which is a fixed value. Needed only by TestCloseInactiveClusters_StopsAtTimeBudget
// below, to simulate elapsed time between batches deterministically.
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestCloseInactiveClusters_SingleBatchWhenFewerThanBatchSize(t *testing.T) {
	repo := &fakeClusterRepo{
		closeInactiveBatchFunc: func(time.Time, string, int, time.Time) (int, error) {
			return 3, nil // fewer than BatchSize (10) -> loop stops after one batch
		},
	}
	clock := fakeClock{now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	cfg := in.CloseInactiveClustersConfig{CloseAfter: 96 * time.Hour, BatchSize: 10, MaxBatchesPerRun: 5, TimeBudget: time.Minute, CloseReason: "inactivity_ttl"}
	uc := usecase.NewCloseInactiveClusters(repo, clock, fakeLogger{}, cfg)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ClustersClosed != 3 {
		t.Errorf("ClustersClosed = %d, want 3", result.ClustersClosed)
	}
	if result.BatchesRun != 1 {
		t.Errorf("BatchesRun = %d, want 1", result.BatchesRun)
	}
}

func TestCloseInactiveClusters_StopsAtMaxBatchesPerRun(t *testing.T) {
	calls := 0
	repo := &fakeClusterRepo{
		closeInactiveBatchFunc: func(time.Time, string, int, time.Time) (int, error) {
			calls++
			return 10, nil // always a full batch -> would loop forever without the cap
		},
	}
	clock := fakeClock{now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	cfg := in.CloseInactiveClustersConfig{CloseAfter: 96 * time.Hour, BatchSize: 10, MaxBatchesPerRun: 3, TimeBudget: time.Minute, CloseReason: "inactivity_ttl"}
	uc := usecase.NewCloseInactiveClusters(repo, clock, fakeLogger{}, cfg)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.BatchesRun != 3 {
		t.Errorf("BatchesRun = %d, want 3 (MaxBatchesPerRun cap)", result.BatchesRun)
	}
	if result.ClustersClosed != 30 {
		t.Errorf("ClustersClosed = %d, want 30", result.ClustersClosed)
	}
	if calls != 3 {
		t.Errorf("CloseInactiveBatch calls = %d, want 3", calls)
	}
}

func TestCloseInactiveClusters_StopsAtTimeBudget(t *testing.T) {
	clock := &mutableClock{now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	repo := &fakeClusterRepo{
		closeInactiveBatchFunc: func(time.Time, string, int, time.Time) (int, error) {
			clock.Advance(30 * time.Second)
			return 10, nil // always full -> only the time budget stops this loop
		},
	}
	cfg := in.CloseInactiveClustersConfig{CloseAfter: 96 * time.Hour, BatchSize: 10, MaxBatchesPerRun: 100, TimeBudget: 45 * time.Second, CloseReason: "inactivity_ttl"}
	uc := usecase.NewCloseInactiveClusters(repo, clock, fakeLogger{}, cfg)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// deadline = start+45s. Batch 1 runs (elapsed 0s < 45s), clock -> +30s.
	// Batch 2 runs (elapsed 30s < 45s), clock -> +60s. Batch 3 does not run
	// (elapsed 60s >= 45s).
	if result.BatchesRun != 2 {
		t.Errorf("BatchesRun = %d, want 2 (time budget cap)", result.BatchesRun)
	}
}

func TestCloseInactiveClusters_PropagatesError(t *testing.T) {
	wantErr := errors.New("db unavailable")
	repo := &fakeClusterRepo{
		closeInactiveBatchFunc: func(time.Time, string, int, time.Time) (int, error) {
			return 0, wantErr
		},
	}
	clock := fakeClock{now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	cfg := in.CloseInactiveClustersConfig{CloseAfter: 96 * time.Hour, BatchSize: 10, MaxBatchesPerRun: 5, TimeBudget: time.Minute, CloseReason: "inactivity_ttl"}
	uc := usecase.NewCloseInactiveClusters(repo, clock, fakeLogger{}, cfg)

	result, err := uc.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want wrapping %v", err, wantErr)
	}
	if result.BatchesRun != 0 || result.ClustersClosed != 0 {
		t.Errorf("result on immediate error = %+v, want zero value", result)
	}
}

func TestCloseInactiveClusters_RepeatedRunIsIdempotent(t *testing.T) {
	repo := &fakeClusterRepo{
		closeInactiveBatchFunc: func(time.Time, string, int, time.Time) (int, error) {
			return 0, nil // nothing eligible
		},
	}
	clock := fakeClock{now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	cfg := in.CloseInactiveClustersConfig{CloseAfter: 96 * time.Hour, BatchSize: 10, MaxBatchesPerRun: 5, TimeBudget: time.Minute, CloseReason: "inactivity_ttl"}
	uc := usecase.NewCloseInactiveClusters(repo, clock, fakeLogger{}, cfg)

	for i := 0; i < 2; i++ {
		result, err := uc.Run(context.Background())
		if err != nil {
			t.Fatalf("Run() #%d error = %v", i, err)
		}
		if result.ClustersClosed != 0 || result.BatchesRun != 1 {
			t.Errorf("Run() #%d = %+v, want {0 1}", i, result)
		}
	}
}
