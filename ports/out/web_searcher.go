// ports/out/web_searcher.go
package out

import (
	"context"
	"errors"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

// SearchOptions bounds one WebSearcher.Search call:
// the application enforces these limits before and after calling the
// adapter — the LLM never controls them directly.
type SearchOptions struct {
	// MaxResults caps how many results the adapter may return.
	MaxResults int
	// TrustedDomains restricts results to these domains (exact host
	// match or subdomain of one of these entries). The adapter is
	// responsible for filtering — an untrusted-domain result must never
	// be returned from Search.
	TrustedDomains []string
}

// WebSearcher is the driven port to a provider-independent web search
// API, called only by the offline catalog-verification tool loop
// (app/catalogverification) — never by the ingestion
// pipeline (the service binary) or the online entity-resolution fallback
// (app/entityresolution). Provider selection is
// config-driven (catalog_maintenance.web_lookup.provider); this
// interface exists so no application code depends on one search vendor.
type WebSearcher interface {
	Search(ctx context.Context, query string, opts SearchOptions) ([]weblookup.SearchResult, error)
}

// ErrSearchProviderUnavailable means the search provider could not answer
// (for example every upstream engine is rate-limited or behind a CAPTCHA),
// as opposed to answering with genuinely no results. Callers must treat it
// as an outage — never as "no evidence" — and must not cache the outcome.
var ErrSearchProviderUnavailable = errors.New("web search provider unavailable")
