// Package entitycycle runs the entity step of the interactive editorial cycle:
// it mines entities from the articles of one review session, verifies them,
// lets the operator approve them and regenerates the catalog. It is offline
// tooling and must never be linked into the ingestion binary.
package entitycycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// ArticleFinder loads one stored article.
type ArticleFinder interface {
	FindByID(ctx context.Context, id string) (article.Article, error)
}

// ProcessedFilter reports which of the given articles have not yet been
// processed by entity discovery for an extractor version.
type ProcessedFilter interface {
	FilterUnprocessed(ctx context.Context, extractorVersion string, articleIDs []string) ([]string, error)
}

// SessionLister is an entitydiscovery.ArticleLister limited to the articles of
// one review session. The time bound passed by Discover is ignored: the session
// defines the set.
type SessionLister struct {
	IDs       []string
	Articles  ArticleFinder
	Processed ProcessedFilter
}

// ListSince returns every session article (the --force path of Discover).
func (l SessionLister) ListSince(ctx context.Context, _ time.Time, limit int) ([]article.Article, error) {
	return l.load(ctx, l.IDs, limit)
}

// ListUnprocessedByEntityDiscovery returns the session articles that have no
// processed marker for extractorVersion, oldest first, after applying limit.
func (l SessionLister) ListUnprocessedByEntityDiscovery(ctx context.Context, _ time.Time, extractorVersion string, limit int) ([]article.Article, error) {
	pending, err := l.Processed.FilterUnprocessed(ctx, extractorVersion, l.IDs)
	if err != nil {
		return nil, fmt.Errorf("entitycycle: filter processed articles: %w", err)
	}
	return l.load(ctx, pending, limit)
}

func (l SessionLister) load(ctx context.Context, ids []string, limit int) ([]article.Article, error) {
	seen := make(map[string]bool, len(ids))
	var out []article.Article
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		a, err := l.Articles.FindByID(ctx, id)
		if errors.Is(err, article.ErrNotFound) {
			continue // purged by retention since the review
		}
		if err != nil {
			return nil, fmt.Errorf("entitycycle: load article %s: %w", id, err)
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].FetchedAt.Equal(out[j].FetchedAt) {
			return out[i].FetchedAt.Before(out[j].FetchedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
