// Package knowledgecache decorates a ports/out.KnowledgeSource with
// SQLite-backed positive/negative caching, an explicit stale-on-transient-
// error fallback, and visible metrics. It makes frequently
// resolved entities (for example Patrick Ostrander) effectively local after
// the first lookup.
package knowledgecache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Config tunes Cached. NegativeTTL should not exceed PositiveTTL.
type Config struct {
	Provider    string
	Language    string
	PositiveTTL time.Duration
	NegativeTTL time.Duration
	// AllowStaleOnError lets an expired entry answer a request only when
	// the inner source fails with an error wrapping out.ErrTransient.
	AllowStaleOnError bool
}

// Stats are cumulative cache metrics. Callers log them at the end of a
// run. NegativeHits is a subset of the hit counters.
type Stats struct {
	SearchHits   int64
	SearchMisses int64
	EntityHits   int64
	EntityMisses int64
	NegativeHits int64
	StaleServed  int64
	// NetworkCalls counts calls made to the wrapped source (one per cache
	// miss batch), not HTTP requests: client retries and 50-ID chunking
	// are not counted.
	NetworkCalls int64
}

// Cached implements out.KnowledgeSource over another source.
type Cached struct {
	inner out.KnowledgeSource
	cache out.KnowledgeCache
	clock out.Clock
	cfg   Config

	mu    sync.Mutex
	stats Stats
}

var _ out.KnowledgeSource = (*Cached)(nil)

// New builds a Cached source.
func New(inner out.KnowledgeSource, cache out.KnowledgeCache, clock out.Clock, cfg Config) *Cached {
	return &Cached{inner: inner, cache: cache, clock: clock, cfg: cfg}
}

// Stats returns a snapshot of the metrics.
func (c *Cached) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func (c *Cached) bump(f func(*Stats)) {
	c.mu.Lock()
	f(&c.stats)
	c.mu.Unlock()
}

// Purge deletes cache rows that expired more than retention ago.
func (c *Cached) Purge(ctx context.Context, retention time.Duration) (int64, error) {
	return c.cache.Purge(ctx, c.clock.Now().Add(-retention))
}

func normalizeQuery(q string) string {
	return strings.ToLower(strings.Join(strings.Fields(q), " "))
}

// Search implements out.KnowledgeSource.
func (c *Cached) Search(ctx context.Context, query string, limit int) ([]knowledge.Candidate, error) {
	norm := normalizeQuery(query)
	if norm == "" || limit <= 0 {
		return nil, nil
	}
	key := out.SearchKey{Provider: c.cfg.Provider, Language: c.cfg.Language, Limit: limit, Query: norm}
	now := c.clock.Now()

	entry, found, err := c.cache.GetSearch(ctx, key)
	if err != nil {
		return nil, err
	}
	if found && entry.ExpiresAt.After(now) {
		c.bump(func(s *Stats) {
			s.SearchHits++
			if entry.Negative {
				s.NegativeHits++
			}
		})
		return entry.Candidates, nil
	}
	c.bump(func(s *Stats) { s.SearchMisses++; s.NetworkCalls++ })

	candidates, err := c.inner.Search(ctx, query, limit)
	if err != nil {
		if found && c.cfg.AllowStaleOnError && errors.Is(err, out.ErrTransient) {
			c.bump(func(s *Stats) { s.StaleServed++ })
			return entry.Candidates, nil
		}
		return nil, err
	}

	negative := len(candidates) == 0
	ttl := c.cfg.PositiveTTL
	if negative {
		ttl = c.cfg.NegativeTTL
	}
	put := out.CachedSearch{Candidates: candidates, Negative: negative, ExpiresAt: now.Add(ttl)}
	if err := c.cache.PutSearch(ctx, key, put, now); err != nil {
		return nil, err
	}
	return candidates, nil
}

// Get implements out.KnowledgeSource. Only ids without a fresh cache entry
// are sent to the inner source, in a single call.
func (c *Cached) Get(ctx context.Context, ids []string) ([]knowledge.Entity, error) {
	now := c.clock.Now()

	ordered := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			ordered = append(ordered, id)
		}
	}

	resolved := make(map[string]knowledge.Entity, len(ordered))
	stale := make(map[string]out.CachedEntity)
	var missing []string
	for _, id := range ordered {
		entry, found, err := c.cache.GetEntity(ctx, c.entityKey(id))
		if err != nil {
			return nil, err
		}
		if found && entry.ExpiresAt.After(now) {
			c.bump(func(s *Stats) {
				s.EntityHits++
				if entry.Negative {
					s.NegativeHits++
				}
			})
			if !entry.Negative {
				resolved[id] = entry.Entity
			}
			continue
		}
		if found {
			stale[id] = entry
		}
		missing = append(missing, id)
		c.bump(func(s *Stats) { s.EntityMisses++ })
	}

	if len(missing) > 0 {
		c.bump(func(s *Stats) { s.NetworkCalls++ })
		fetched, err := c.inner.Get(ctx, missing)
		if err != nil {
			if !c.cfg.AllowStaleOnError || !errors.Is(err, out.ErrTransient) || len(stale) != len(missing) {
				return nil, err
			}
			for _, id := range missing {
				c.bump(func(s *Stats) { s.StaleServed++ })
				if !stale[id].Negative {
					resolved[id] = stale[id].Entity
				}
			}
		} else {
			byID := make(map[string]knowledge.Entity, len(fetched))
			for _, e := range fetched {
				byID[e.ID] = e
			}
			for _, id := range missing {
				e, ok := byID[id]
				ttl := c.cfg.PositiveTTL
				if !ok {
					ttl = c.cfg.NegativeTTL
				}
				put := out.CachedEntity{Entity: e, Negative: !ok, ExpiresAt: now.Add(ttl)}
				if err := c.cache.PutEntity(ctx, c.entityKey(id), put, now); err != nil {
					return nil, err
				}
				if ok {
					resolved[id] = e
				}
			}
		}
	}

	result := make([]knowledge.Entity, 0, len(resolved))
	for _, id := range ordered {
		if e, ok := resolved[id]; ok {
			result = append(result, e)
		}
	}
	return result, nil
}

func (c *Cached) entityKey(id string) out.EntityKey {
	return out.EntityKey{Provider: c.cfg.Provider, Language: c.cfg.Language, ID: id}
}
