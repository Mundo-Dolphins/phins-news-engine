// Package websearch holds WebSearcher adapters. NewSearXNG
// (searxng.go) is the bundled real implementation, selected
// via New when catalog_maintenance.web_lookup.provider is "searxng".
// NewNoop remains available as an explicit no-provider-configured
// placeholder: any "web_search" action the local LLM requests simply
// fails, and catalogverification.Service's fail-open contract degrades
// that candidate to unresolved rather than aborting the run.
package websearch

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type noop struct{}

// NewNoop builds a ports/out.WebSearcher that always returns an error.
func NewNoop() appout.WebSearcher {
	return noop{}
}

func (noop) Search(_ context.Context, _ string, _ appout.SearchOptions) ([]weblookup.SearchResult, error) {
	return nil, fmt.Errorf("websearch: no provider configured")
}
