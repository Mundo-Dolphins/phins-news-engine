package knowledgecache_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/knowledgecache"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

type memCache struct {
	searches     map[string]out.CachedSearch
	entities     map[out.EntityKey]out.CachedEntity
	purgedBefore time.Time
}

func newMemCache() *memCache {
	return &memCache{searches: map[string]out.CachedSearch{}, entities: map[out.EntityKey]out.CachedEntity{}}
}

func (m *memCache) GetSearch(_ context.Context, k out.SearchKey) (out.CachedSearch, bool, error) {
	v, ok := m.searches[k.Hash()]
	return v, ok, nil
}

func (m *memCache) PutSearch(_ context.Context, k out.SearchKey, v out.CachedSearch, _ time.Time) error {
	m.searches[k.Hash()] = v
	return nil
}

func (m *memCache) GetEntity(_ context.Context, k out.EntityKey) (out.CachedEntity, bool, error) {
	v, ok := m.entities[k]
	return v, ok, nil
}

func (m *memCache) PutEntity(_ context.Context, k out.EntityKey, v out.CachedEntity, _ time.Time) error {
	m.entities[k] = v
	return nil
}

func (m *memCache) Purge(_ context.Context, before time.Time) (int64, error) {
	m.purgedBefore = before
	return 7, nil
}

type fakeInner struct {
	searchCalls int
	getCalls    [][]string
	search      func(q string) ([]knowledge.Candidate, error)
	entities    map[string]knowledge.Entity
	getErr      error
}

func (f *fakeInner) Search(_ context.Context, q string, _ int) ([]knowledge.Candidate, error) {
	f.searchCalls++
	return f.search(q)
}

func (f *fakeInner) Get(_ context.Context, ids []string) ([]knowledge.Entity, error) {
	f.getCalls = append(f.getCalls, append([]string(nil), ids...))
	if f.getErr != nil {
		return nil, f.getErr
	}
	var res []knowledge.Entity
	for _, id := range ids {
		if e, ok := f.entities[id]; ok {
			res = append(res, e)
		}
	}
	return res, nil
}

var errTransient = fmt.Errorf("boom: %w", out.ErrTransient)

func setup(allowStale bool) (*knowledgecache.Cached, *fakeInner, *memCache, *fakeClock) {
	inner := &fakeInner{
		search: func(string) ([]knowledge.Candidate, error) {
			return []knowledge.Candidate{{ID: "Q1", Label: "Patrick Ostrander"}}, nil
		},
		entities: map[string]knowledge.Entity{"Q1": {ID: "Q1", Label: "One"}, "Q2": {ID: "Q2", Label: "Two"}},
	}
	cache := newMemCache()
	clock := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	c := knowledgecache.New(inner, cache, clock, knowledgecache.Config{
		Provider: "wikidata", Language: "en", PositiveTTL: 24 * time.Hour, NegativeTTL: time.Hour, AllowStaleOnError: allowStale,
	})
	return c, inner, cache, clock
}

func TestSearch_RepeatedQueryHitsInnerOnceAndNormalizes(t *testing.T) {
	c, inner, _, _ := setup(false)
	ctx := context.Background()
	for _, q := range []string{"Patrick Ostrander", "  patrick   OSTRANDER ", "patrick ostrander"} {
		got, err := c.Search(ctx, q, 5)
		if err != nil || len(got) != 1 || got[0].ID != "Q1" {
			t.Fatalf("Search(%q) = %v, %v", q, got, err)
		}
	}
	if inner.searchCalls != 1 {
		t.Fatalf("inner searches = %d, want 1", inner.searchCalls)
	}
	s := c.Stats()
	if s.SearchHits != 2 || s.SearchMisses != 1 || s.NetworkCalls != 1 {
		t.Fatalf("Stats = %+v", s)
	}
}

func TestSearch_EmptyQueryOrLimitSkipsInnerAndCache(t *testing.T) {
	c, inner, cache, _ := setup(false)
	if got, err := c.Search(context.Background(), "  ", 5); got != nil || err != nil {
		t.Fatalf("Search(blank) = %v, %v", got, err)
	}
	if got, err := c.Search(context.Background(), "x", 0); got != nil || err != nil {
		t.Fatalf("Search(limit 0) = %v, %v", got, err)
	}
	if inner.searchCalls != 0 || len(cache.searches) != 0 {
		t.Fatalf("inner=%d cache=%d, want 0/0", inner.searchCalls, len(cache.searches))
	}
}

func TestSearch_NegativeResultIsCachedWithShorterTTL(t *testing.T) {
	c, inner, cache, clock := setup(false)
	inner.search = func(string) ([]knowledge.Candidate, error) { return nil, nil }
	ctx := context.Background()

	if got, err := c.Search(ctx, "nobody", 5); err != nil || len(got) != 0 {
		t.Fatalf("Search() = %v, %v", got, err)
	}
	if _, err := c.Search(ctx, "nobody", 5); err != nil {
		t.Fatal(err)
	}
	if inner.searchCalls != 1 {
		t.Fatalf("inner searches = %d, want 1 (negative cached)", inner.searchCalls)
	}
	if c.Stats().NegativeHits != 1 {
		t.Errorf("NegativeHits = %d, want 1", c.Stats().NegativeHits)
	}
	for _, v := range cache.searches {
		if !v.Negative || !v.ExpiresAt.Equal(clock.t.Add(time.Hour)) {
			t.Errorf("stored = %+v, want negative with 1h TTL", v)
		}
	}
}

func TestSearch_ExpiredEntryRefetches(t *testing.T) {
	c, inner, _, clock := setup(false)
	ctx := context.Background()
	_, _ = c.Search(ctx, "ostrander", 5)
	clock.t = clock.t.Add(25 * time.Hour)
	_, _ = c.Search(ctx, "ostrander", 5)
	if inner.searchCalls != 2 {
		t.Fatalf("inner searches = %d, want 2 after expiry", inner.searchCalls)
	}
}

func TestSearch_StaleFallbackOnlyOnTransientError(t *testing.T) {
	c, inner, _, clock := setup(true)
	ctx := context.Background()
	_, _ = c.Search(ctx, "ostrander", 5)
	clock.t = clock.t.Add(48 * time.Hour)

	inner.search = func(string) ([]knowledge.Candidate, error) { return nil, errTransient }
	got, err := c.Search(ctx, "ostrander", 5)
	if err != nil || len(got) != 1 || got[0].ID != "Q1" {
		t.Fatalf("Search() = %v, %v, want stale Q1", got, err)
	}
	if c.Stats().StaleServed != 1 {
		t.Errorf("StaleServed = %d, want 1", c.Stats().StaleServed)
	}

	inner.search = func(string) ([]knowledge.Candidate, error) { return nil, errors.New("HTTP 400") }
	if _, err := c.Search(ctx, "ostrander", 5); err == nil {
		t.Fatal("non-transient error must not be answered from stale cache")
	}
}

func TestSearch_NoStaleWhenDisabledOrNoEntry(t *testing.T) {
	c, inner, _, clock := setup(false)
	ctx := context.Background()
	_, _ = c.Search(ctx, "ostrander", 5)
	clock.t = clock.t.Add(48 * time.Hour)
	inner.search = func(string) ([]knowledge.Candidate, error) { return nil, errTransient }
	if _, err := c.Search(ctx, "ostrander", 5); !errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient when stale is disabled", err)
	}

	c2, inner2, _, _ := setup(true)
	inner2.search = func(string) ([]knowledge.Candidate, error) { return nil, errTransient }
	if _, err := c2.Search(ctx, "never-seen", 5); !errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient when nothing is cached", err)
	}
}

func TestGet_CachesPositiveAndNegativeAndFetchesOnlyMissing(t *testing.T) {
	c, inner, _, _ := setup(false)
	ctx := context.Background()

	got, err := c.Get(ctx, []string{"Q1", "Q404"})
	if err != nil || len(got) != 1 || got[0].ID != "Q1" {
		t.Fatalf("Get() = %v, %v", got, err)
	}
	// Q1 positive, Q404 negative are cached; asking for Q2 too only fetches Q2.
	got, err = c.Get(ctx, []string{"Q2", "Q404", "Q1"})
	if err != nil || len(got) != 2 || got[0].ID != "Q2" || got[1].ID != "Q1" {
		t.Fatalf("Get() = %v, %v, want [Q2 Q1] in input order", got, err)
	}
	if len(inner.getCalls) != 2 || fmt.Sprint(inner.getCalls[1]) != "[Q2]" {
		t.Fatalf("inner Get calls = %v, want second call to fetch only Q2", inner.getCalls)
	}
	s := c.Stats()
	if s.EntityHits != 2 || s.NegativeHits != 1 {
		t.Errorf("Stats = %+v, want 2 entity hits (Q404 negative, Q1) and 1 negative hit", s)
	}
}

func TestGet_FullyCachedMakesNoInnerCall(t *testing.T) {
	c, inner, _, _ := setup(false)
	ctx := context.Background()
	_, _ = c.Get(ctx, []string{"Q1"})
	_, _ = c.Get(ctx, []string{"Q1"})
	if len(inner.getCalls) != 1 {
		t.Fatalf("inner Get calls = %d, want 1", len(inner.getCalls))
	}
}

func TestGet_StaleFallbackRequiresEveryMissingIDToHaveStaleEntry(t *testing.T) {
	c, inner, _, clock := setup(true)
	ctx := context.Background()
	_, _ = c.Get(ctx, []string{"Q1", "Q2"})
	clock.t = clock.t.Add(48 * time.Hour)
	inner.getErr = errTransient

	got, err := c.Get(ctx, []string{"Q1", "Q2"})
	if err != nil || len(got) != 2 {
		t.Fatalf("Get() = %v, %v, want both entities served stale", got, err)
	}
	if c.Stats().StaleServed != 2 {
		t.Errorf("StaleServed = %d, want 2", c.Stats().StaleServed)
	}

	// Q3 has never been cached: serving a partial answer would mislead, so the error surfaces.
	if _, err := c.Get(ctx, []string{"Q1", "Q3"}); !errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient when some ids have no cache entry", err)
	}
}

func TestGet_NonTransientErrorNeverServesStale(t *testing.T) {
	c, inner, _, clock := setup(true)
	ctx := context.Background()
	_, _ = c.Get(ctx, []string{"Q1"})
	clock.t = clock.t.Add(48 * time.Hour)
	inner.getErr = errors.New("HTTP 400")
	if _, err := c.Get(ctx, []string{"Q1"}); err == nil {
		t.Fatal("non-transient error must propagate")
	}
}

func TestPurge_DeletesRowsExpiredBeyondRetention(t *testing.T) {
	c, _, cache, clock := setup(false)
	n, err := c.Purge(context.Background(), 30*24*time.Hour)
	if err != nil || n != 7 {
		t.Fatalf("Purge() = %d, %v", n, err)
	}
	if want := clock.t.Add(-30 * 24 * time.Hour); !cache.purgedBefore.Equal(want) {
		t.Fatalf("purged before %v, want %v", cache.purgedBefore, want)
	}
}
