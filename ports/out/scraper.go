// ports/out/scraper.go
package out

import (
	"context"
	"errors"
	"time"
)

// ScrapeMode selects the fetch strategy requested from the Scraping
// Service. "" (the zero value) means "let the adapter apply its
// domain/default policy" — see the host's out.Scraper implementation.
type ScrapeMode string

const (
	ScrapeModeHTTP    ScrapeMode = "http"
	ScrapeModeBrowser ScrapeMode = "browser"
	ScrapeModeAuto    ScrapeMode = "auto"
)

// ScrapeRequest is the subset of the Scraping Service's ScrapeRequest (see
// its OpenAPI spec, vendored at
// the scraping service's OpenAPI contract) this bot needs.
type ScrapeRequest struct {
	URL  string
	Mode ScrapeMode
	// CacheTTL is 0 to use the service/domain default TTL.
	CacheTTL     time.Duration
	ForceRefresh bool
	// TimeoutSeconds is 0 to use the adapter's configured default.
	TimeoutSeconds int
}

// ScrapeResult is the subset of the Scraping Service's ScrapeResponse this
// bot needs.
type ScrapeResult struct {
	FinalURL   string
	StatusCode int
	FromCache  bool
	HTML       string
	FetchedAt  time.Time
}

// ErrUnauthorized is returned by Scraper.Scrape when the Scraping Service
// rejects the request with HTTP 401 (bad or missing API key).
var ErrUnauthorized = errors.New("scraper: unauthorized")

// ErrRateLimited is returned by Scraper.Scrape when the Scraping Service
// keeps responding HTTP 429 after the adapter's retry budget is exhausted.
var ErrRateLimited = errors.New("scraper: rate limited")

// Scraper is the port to the external Scraping Service. It is never
// implemented by a local browser — see ADR 0011.
type Scraper interface {
	// Scrape fetches req.URL's rendered HTML. Errors wrap ErrUnauthorized
	// or ErrRateLimited when applicable so callers can distinguish them
	// with errors.Is; other failures (5xx, transport, timeout) are plain
	// wrapped errors, already retried internally as far as reasonable.
	Scrape(ctx context.Context, req ScrapeRequest) (ScrapeResult, error)
}
