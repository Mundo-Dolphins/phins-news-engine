// Package compositefetcher composes every configured content source
// (website, rss, reddit, bluesky, twitter, instagram, youtube) into the
// single out.Fetcher the process uses, with per-source scheduling and
// bounded concurrency/fan-in backpressure.
package compositefetcher

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// namedFetcher is the shape every per-platform fetcher package exposes.
type namedFetcher interface {
	Fetch(ctx context.Context) ([]article.Article, error)
}

// Source pairs one platform fetcher with its scheduling and limits.
type Source struct {
	Name             string
	Fetcher          namedFetcher
	Interval         time.Duration
	MaxItemsPerCycle int
}

// Fetcher implements out.Fetcher, composing all configured sources with
// per-source scheduling and bounded concurrency/fan-in backpressure.
type Fetcher struct {
	sources           []Source
	globalConcurrency int
	fanInBuffer       int
	clock             appout.Clock
	logger            appout.Logger

	mu          sync.Mutex
	lastAttempt map[string]time.Time
}

// New builds a ready Fetcher.
func New(sources []Source, globalConcurrency, fanInBuffer int, clock appout.Clock, logger appout.Logger) *Fetcher {
	return &Fetcher{
		sources:           sources,
		globalConcurrency: globalConcurrency,
		fanInBuffer:       fanInBuffer,
		clock:             clock,
		logger:            logger,
		lastAttempt:       make(map[string]time.Time),
	}
}

var _ appout.Fetcher = (*Fetcher)(nil)

// Fetch runs every due source (per its own Interval) concurrently, bounded
// by globalConcurrency, fanning their results into a fanInBuffer-sized
// channel before combining and sorting them ascending by PublishedAt. A
// single source's failure is logged and skipped — it never aborts the
// others, and its lastAttempt still updates so it isn't retried faster
// than its own Interval.
func (f *Fetcher) Fetch(ctx context.Context) ([]article.Article, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := f.clock.Now()
	due := f.dueSources(now)

	results := make(chan []article.Article, f.fanInBuffer)
	sem := make(chan struct{}, f.globalConcurrency)
	var wg sync.WaitGroup

	for _, src := range due {
		wg.Add(1)
		go func(src Source) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			items, err := src.Fetcher.Fetch(ctx)
			f.recordAttempt(src.Name, now)
			if err != nil {
				f.logger.Warn("compositefetcher: source fetch failed", "source", src.Name, "err", err.Error())
				return
			}
			if src.MaxItemsPerCycle > 0 && len(items) > src.MaxItemsPerCycle {
				f.logger.Info("compositefetcher: source truncated", "source", src.Name, "raw_count", len(items), "kept", src.MaxItemsPerCycle)
				items = items[:src.MaxItemsPerCycle]
			}
			f.logger.Info("compositefetcher: source fetched", "source", src.Name, "count", len(items))

			select {
			case results <- items:
			case <-ctx.Done():
			}
		}(src)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var all []article.Article
	for items := range results {
		all = append(all, items...)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool { return all[i].PublishedAt.Before(all[j].PublishedAt) })
	return all, nil
}

// dueSources returns the sources whose Interval has elapsed since their
// last attempt (or that have never been attempted).
func (f *Fetcher) dueSources(now time.Time) []Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	due := make([]Source, 0, len(f.sources))
	for _, src := range f.sources {
		last, attempted := f.lastAttempt[src.Name]
		if !attempted || now.Sub(last) >= src.Interval {
			due = append(due, src)
		}
	}
	return due
}

// recordAttempt marks name as attempted at now, regardless of success.
func (f *Fetcher) recordAttempt(name string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastAttempt[name] = now
}
