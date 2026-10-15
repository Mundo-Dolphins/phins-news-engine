# ADR 0011: The scraper port

## Status

Accepted

## Context

Some sources publish no feed or API, and a feed sometimes fails while its
page still loads. Fetching and rendering arbitrary web pages raises legal,
operational and security questions (terms of use, headless browsers,
proxies, rate limits) that depend on who runs the engine and which sites
they read.

## Decision

The library defines a port and ships no implementation:

```go
type Scraper interface {
	Scrape(ctx context.Context, req ScrapeRequest) (ScrapeResult, error)
}
```

`out.ScrapeRequest` carries the URL, a mode hint (`http`, `browser` or
`auto`), a cache TTL, a force-refresh flag and a timeout.
`out.ScrapeResult` returns the final URL, status code, whether it came from
a cache, the HTML and the fetch time. Errors wrap `out.ErrUnauthorized` or
`out.ErrRateLimited` when those apply, so callers can tell them apart; any
other error is treated as already retried.

A host supplies an implementation in two places:

- `fetcherwiring.Extensions.Scraper`, used by the RSS adapter as a fallback
  when a feed fetch fails, and by host category builders;
- `shared.SetScraperFactory`, used by the dictionary tooling.

The `scraping_service` configuration section is validated by the library
and passed to the host's factory. The RSS adapter never hands a URL that
the network guard refused to the scraper.

## Consequences

- The library contains no scraping code and no browser dependency.
- With nothing registered, the RSS fallback is off and the dictionary
  commands that need page content fail with `shared.ErrNoScraper`.
- The host is responsible for what its scraper fetches and how.
