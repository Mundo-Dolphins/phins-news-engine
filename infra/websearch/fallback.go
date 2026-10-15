// infra/websearch/fallback.go
package websearch

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fallbackChain struct {
	primary, secondary appout.WebSearcher
	maxSecondary       int
	usedSecondary      int
	onFallback         func(err error)
}

// NewFallbackChain returns a WebSearcher that asks primary and, only when it
// reports appout.ErrSearchProviderUnavailable, asks secondary — at most
// maxSecondary times for the lifetime of the returned value (a per-run
// budget protecting a metered quota). Not safe for concurrent use, like
// catalogverification.Service which owns it.
func NewFallbackChain(primary, secondary appout.WebSearcher, maxSecondary int, onFallback func(err error)) appout.WebSearcher {
	return &fallbackChain{primary: primary, secondary: secondary, maxSecondary: maxSecondary, onFallback: onFallback}
}

func (c *fallbackChain) Search(ctx context.Context, query string, opts appout.SearchOptions) ([]weblookup.SearchResult, error) {
	res, err := c.primary.Search(ctx, query, opts)
	if err == nil || !errors.Is(err, appout.ErrSearchProviderUnavailable) {
		return res, err
	}
	if c.usedSecondary >= c.maxSecondary {
		return nil, fmt.Errorf("websearch: primary unavailable and secondary budget (%d) exhausted: %w", c.maxSecondary, appout.ErrSearchProviderUnavailable)
	}
	c.usedSecondary++
	if c.onFallback != nil {
		c.onFallback(err)
	}
	return c.secondary.Search(ctx, query, opts)
}
