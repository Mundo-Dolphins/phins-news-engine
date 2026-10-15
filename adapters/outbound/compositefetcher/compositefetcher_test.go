package compositefetcher_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/compositefetcher"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)        {}
func (noopLogger) Info(string, ...any)         {}
func (noopLogger) Warn(string, ...any)         {}
func (noopLogger) Error(string, error, ...any) {}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeFetcher struct {
	mu      sync.Mutex
	calls   int
	items   []article.Article
	err     error
	started chan struct{}
	release chan struct{}
}

func (f *fakeFetcher) Fetch(ctx context.Context) ([]article.Article, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.items, f.err
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// blockingFetcher blocks until release is closed, ignoring ctx entirely
// (unlike fakeFetcher, which races release against ctx.Done()). It exists
// so a test can hold the composite fetcher's semaphore slot open for a
// controlled duration that is independent of ctx cancellation, isolating
// the semaphore-wait race under test in another, still-queued source from
// any race in this source's own cancellation handling.
type blockingFetcher struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (f *blockingFetcher) Fetch(context.Context) ([]article.Article, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	close(f.started)
	<-f.release
	return nil, nil
}

func (f *blockingFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestFetch_SkipsSourceNotYetDue(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	ff := &fakeFetcher{}
	f := compositefetcher.New([]compositefetcher.Source{
		{Name: "src", Fetcher: ff, Interval: 10 * time.Minute, MaxItemsPerCycle: 10},
	}, 4, 8, clock, noopLogger{})

	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if ff.callCount() != 1 {
		t.Fatalf("calls = %d, want 1 (first call is always due)", ff.callCount())
	}

	clock.now = clock.now.Add(2 * time.Minute)
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if ff.callCount() != 1 {
		t.Fatalf("calls = %d, want still 1 (not due yet at +2min of a 10min interval)", ff.callCount())
	}

	clock.now = clock.now.Add(9 * time.Minute)
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if ff.callCount() != 2 {
		t.Fatalf("calls = %d, want 2 (due at +11min total)", ff.callCount())
	}
}

func TestFetch_RecordsAttemptEvenOnFailure(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	ff := &fakeFetcher{err: errors.New("boom")}
	f := compositefetcher.New([]compositefetcher.Source{
		{Name: "src", Fetcher: ff, Interval: time.Minute, MaxItemsPerCycle: 10},
	}, 4, 8, clock, noopLogger{})

	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v, want nil (one source's failure must not abort the run)", err)
	}
	clock.now = clock.now.Add(30 * time.Second)
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if ff.callCount() != 1 {
		t.Fatalf("calls = %d, want still 1 (failure recorded lastAttempt, source not due again at +30s of a 1min interval)", ff.callCount())
	}
}

func TestFetch_TruncatesToMaxItemsPerCycle(t *testing.T) {
	items := make([]article.Article, 5)
	for i := range items {
		items[i] = article.Article{Title: "x", PublishedAt: time.Now()}
	}
	clock := &fakeClock{now: time.Now()}
	ff := &fakeFetcher{items: items}
	f := compositefetcher.New([]compositefetcher.Source{
		{Name: "src", Fetcher: ff, Interval: time.Minute, MaxItemsPerCycle: 3},
	}, 4, 8, clock, noopLogger{})

	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 3 {
		t.Fatalf("articles = %d, want 3 (truncated from 5)", len(articles))
	}
}

func TestFetch_RespectsGlobalConcurrency(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	sources := make([]compositefetcher.Source, 3)
	for i := range sources {
		sources[i] = compositefetcher.Source{
			Name:             string(rune('a' + i)),
			Fetcher:          &fakeFetcher{started: started, release: release},
			Interval:         time.Minute,
			MaxItemsPerCycle: 10,
		}
	}
	clock := &fakeClock{now: time.Now()}
	f := compositefetcher.New(sources, 1, 8, clock, noopLogger{})

	done := make(chan struct{})
	go func() {
		_, _ = f.Fetch(context.Background())
		close(done)
	}()

	<-started
	select {
	case <-started:
		t.Fatal("a second source started before the first (concurrency=1) was released")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
}

func TestFetch_NoDeadlockWithSmallFanInBuffer(t *testing.T) {
	sources := make([]compositefetcher.Source, 5)
	for i := range sources {
		sources[i] = compositefetcher.Source{
			Name:             string(rune('a' + i)),
			Fetcher:          &fakeFetcher{items: []article.Article{{Title: "x", PublishedAt: time.Now()}}},
			Interval:         time.Minute,
			MaxItemsPerCycle: 10,
		}
	}
	clock := &fakeClock{now: time.Now()}
	f := compositefetcher.New(sources, 5, 1, clock, noopLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	articles, err := f.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 5 {
		t.Fatalf("articles = %d, want 5 (one per source, no deadlock with fan_in_buffer=1)", len(articles))
	}
}

func TestFetch_PartialFailureStillReturnsSuccessfulSources(t *testing.T) {
	good := &fakeFetcher{items: []article.Article{{Title: "good", PublishedAt: time.Now()}}}
	bad := &fakeFetcher{err: errors.New("boom")}
	clock := &fakeClock{now: time.Now()}
	f := compositefetcher.New([]compositefetcher.Source{
		{Name: "bad", Fetcher: bad, Interval: time.Minute, MaxItemsPerCycle: 10},
		{Name: "good", Fetcher: good, Interval: time.Minute, MaxItemsPerCycle: 10},
	}, 4, 8, clock, noopLogger{})

	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v, want nil", err)
	}
	if len(articles) != 1 || articles[0].Title != "good" {
		t.Fatalf("articles = %+v, want only the good source's article", articles)
	}
}

func TestFetch_SortsAscendingByPublishedAt(t *testing.T) {
	now := time.Now()
	newer := &fakeFetcher{items: []article.Article{{Title: "newer", PublishedAt: now}}}
	older := &fakeFetcher{items: []article.Article{{Title: "older", PublishedAt: now.Add(-time.Hour)}}}
	clock := &fakeClock{now: now}
	f := compositefetcher.New([]compositefetcher.Source{
		{Name: "newer", Fetcher: newer, Interval: time.Minute, MaxItemsPerCycle: 10},
		{Name: "older", Fetcher: older, Interval: time.Minute, MaxItemsPerCycle: 10},
	}, 4, 8, clock, noopLogger{})

	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 2 || articles[0].Title != "older" || articles[1].Title != "newer" {
		t.Fatalf("articles = %+v, want [older, newer]", articles)
	}
}

func TestFetch_ContextCancellationPropagates(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	f := compositefetcher.New(nil, 4, 8, clock, noopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch() error = %v, want context.Canceled", err)
	}
}

// TestFetch_SemaphoreWaitPreemptedByCancellation exercises the per-source
// goroutine's `select { case sem <- struct{}{}: ...; case <-ctx.Done():
// return }` branch: a source still queued for the semaphore when ctx is
// cancelled must return without ever calling its Fetcher.Fetch and without
// recordAttempt ever running for it, leaving it due again on the very next
// Fetch call.
//
// Determinism: two sources ("a" and "b") are both blockingFetchers, which
// (unlike fakeFetcher) do not race their release gate against ctx.Done() —
// each only unblocks when the test closes its own release channel,
// regardless of cancellation. With globalConcurrency=1, exactly one of
// them can win the sole semaphore slot; blockingFetcher.Fetch is only
// invoked by the per-source goroutine *after* that goroutine's own
// `sem <- struct{}{}` succeeds, so whichever of a.started/b.started fires
// first identifies the winner unambiguously — the test does not assume a
// fixed source ordering wins the race, it observes it. Once the winner is
// identified, it provably owns the slot and (being a blockingFetcher)
// cannot release it until the test closes its release channel — so the
// loser's own `sem <- struct{}{}` case cannot become ready no matter when
// its goroutine is scheduled during the sleep below. Whenever the loser's
// select does run in that window, ctx.Done() (already ready, since the
// test cancels before sleeping) is the *only* possible outcome — not a
// probabilistic race between two simultaneously-ready cases.
func TestFetch_SemaphoreWaitPreemptedByCancellation(t *testing.T) {
	a := &blockingFetcher{started: make(chan struct{}), release: make(chan struct{})}
	b := &blockingFetcher{started: make(chan struct{}), release: make(chan struct{})}

	clock := &fakeClock{now: time.Now()}
	f := compositefetcher.New([]compositefetcher.Source{
		{Name: "a", Fetcher: a, Interval: time.Minute, MaxItemsPerCycle: 10},
		{Name: "b", Fetcher: b, Interval: time.Minute, MaxItemsPerCycle: 10},
	}, 1, 8, clock, noopLogger{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = f.Fetch(ctx)
		close(done)
	}()

	// Exactly one of a/b wins the sole semaphore slot. Observe which.
	var winner, loser *blockingFetcher
	select {
	case <-a.started:
		winner, loser = a, b
	case <-b.started:
		winner, loser = b, a
	}

	cancel() // loser's outer select now has ctx.Done() as its only possible
	// ready case, for as long as winner keeps holding the slot.

	// Generous window for loser's goroutine to actually be scheduled and
	// resolve its select. This does not introduce a correctness race: the
	// wrong branch (sem <- struct{}{}) is unreachable for loser until
	// winner.release is closed below, which the test does not do yet.
	time.Sleep(200 * time.Millisecond)

	close(winner.release) // let winner finish, freeing the slot so Fetch
	// can return.
	close(loser.release) // loser's Fetch is never expected to run in this
	// cycle, but pre-closing avoids a deadlock on the follow-up Fetch call
	// below, where loser is due again and does run.
	<-done

	if loser.callCount() != 0 {
		t.Fatalf("loser.calls = %d, want 0 (must never acquire the semaphore, nor have its Fetch called, once ctx was cancelled while still waiting for it)", loser.callCount())
	}

	// loser's lastAttempt must never have been recorded (it was preempted
	// before ever running its Fetch), so on a fresh, uncancelled Fetch call
	// with the fake clock unchanged it must still be due; winner (already
	// attempted) must not be due again within its interval.
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if loser.callCount() != 1 {
		t.Fatalf("loser.calls = %d, want 1 (source preempted by cancellation must remain due on the next Fetch)", loser.callCount())
	}
	if winner.callCount() != 1 {
		t.Fatalf("winner.calls = %d, want still 1 (source that actually ran must not be due again within its interval)", winner.callCount())
	}
}

// TestFetch_MidCycleCancellationDiscardsPartialResults exercises the
// `if err := ctx.Err(); err != nil { return nil, err }` check that runs
// after the results-drain loop, before the final sort: it must discard
// any results already produced by sources that completed before ctx was
// cancelled, not return them as a partial slice.
//
// Determinism: "fast" has no release gate, so once the test observes
// fast.started it is a handful of non-blocking, in-goroutine steps
// (recordAttempt, the truncation check, two logger calls, and a buffered,
// always-ready `results <- items` send given fanInBuffer=8 for 2 sources)
// away from having pushed its item onto the results channel — no external
// signal is required for that to complete, so a short sleep before
// cancelling gives it an enormous safety margin. "slow" has a release
// gate it never gets past: it blocks in fakeFetcher's own
// `select { case <-f.release: case <-ctx.Done(): }` until the test
// cancels ctx, at which point it returns ctx.Err() and — because err != nil
// — its outer per-source goroutine returns before ever reaching its own
// results send. So exactly one source's item is known to be sitting in
// the results channel when ctx is cancelled, and Fetch must still discard
// it.
func TestFetch_MidCycleCancellationDiscardsPartialResults(t *testing.T) {
	fastStarted := make(chan struct{})
	fast := &fakeFetcher{
		started: fastStarted,
		items:   []article.Article{{Title: "fast", PublishedAt: time.Now()}},
	}

	slowStarted := make(chan struct{})
	slowRelease := make(chan struct{}) // never closed: slow only unblocks via ctx.Done()
	slow := &fakeFetcher{started: slowStarted, release: slowRelease}

	clock := &fakeClock{now: time.Now()}
	f := compositefetcher.New([]compositefetcher.Source{
		{Name: "fast", Fetcher: fast, Interval: time.Minute, MaxItemsPerCycle: 10},
		{Name: "slow", Fetcher: slow, Interval: time.Minute, MaxItemsPerCycle: 10},
	}, 2, 8, clock, noopLogger{})

	ctx, cancel := context.WithCancel(context.Background())
	type fetchResult struct {
		articles []article.Article
		err      error
	}
	resultCh := make(chan fetchResult, 1)
	go func() {
		articles, err := f.Fetch(ctx)
		resultCh <- fetchResult{articles, err}
	}()

	<-fastStarted
	<-slowStarted

	// Let fast's already-unblocked goroutine finish its non-blocking tail
	// and push its item onto the (buffered, non-blocking) results channel
	// before we cancel.
	time.Sleep(50 * time.Millisecond)

	cancel() // slow, still parked in its release/ctx.Done() select, observes
	// ctx.Done() and returns ctx.Err() without ever sending to results.

	res := <-resultCh
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("Fetch() error = %v, want context.Canceled", res.err)
	}
	if res.articles != nil {
		t.Fatalf("articles = %+v, want nil (a cancelled cycle must discard already-completed sources' results, not return them partially)", res.articles)
	}
}
