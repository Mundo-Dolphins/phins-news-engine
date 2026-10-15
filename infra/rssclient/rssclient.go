// Package rssclient is a shared HTTP+gofeed helper (retry with backoff,
// Retry-After on 429, bounded response reads) used by every fetcher that
// consumes an RSS/Atom feed directly or via a local bridge: rss, reddit,
// instagram, and youtube. twitter parses its bridge response with gofeed
// directly instead, since its per-base proxy-fallback retry loop doesn't
// fit this package's shape. rssclient is not a port — it has no circuit
// breaker or per-domain concurrency, unlike a host scraper adapter's
// client, since none of its callers need it.
package rssclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/mmcdole/gofeed"
)

// Config tunes Fetch's HTTP client, retry budget, and response size limit.
// A zero Timeout means defaultTimeout and a non-positive
// ResponseLimitBytes means netguard.FeedBodyLimit.
type Config struct {
	Timeout            time.Duration
	MaxRetries         int
	BackoffBase        time.Duration
	BackoffMax         time.Duration
	ResponseLimitBytes int64
	// AllowPrivateHosts lets Fetch reach loopback and private-network
	// addresses (an operator-run RSS bridge, say). It defaults to false:
	// feed URLs come from a source catalog, so loopback, RFC 1918, CGNAT
	// and link-local destinations are refused — checked on the URL and
	// again on every resolved address at dial time. When it is true,
	// link-local addresses and the known cloud metadata endpoints
	// (netguard's deny list) are still refused; note that the environment
	// proxy is then honoured (see docs/SECURITY.md).
	AllowPrivateHosts bool
}

// defaultTimeout applies when Config.Timeout is not positive.
const defaultTimeout = 30 * time.Second

func (c Config) limit() int64 {
	if c.ResponseLimitBytes <= 0 {
		return netguard.FeedBodyLimit
	}
	return c.ResponseLimitBytes
}

// newHTTPClient builds the guarded client for one Fetch call: a timeout,
// the netguard redirect policy and a transport that enforces the host
// policy at dial time. The environment proxy stays disabled, as before.
func newHTTPClient(cfg Config) *http.Client {
	tr := netguard.NewTransport(netguard.PolicyFor(cfg.AllowPrivateHosts))
	tr.Proxy = nil
	tr.MaxIdleConnsPerHost = 4
	tr.IdleConnTimeout = 90 * time.Second
	return &http.Client{
		Timeout:       netguard.TimeoutOr(cfg.Timeout, defaultTimeout),
		Transport:     tr,
		CheckRedirect: netguard.CheckRedirect,
	}
}

// Use a real desktop-browser User-Agent for direct feed requests. Several
// publishers reject library-identifying agents even though the feed is
// otherwise publicly reachable over plain HTTP.
const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Fetch retrieves and parses the RSS/Atom feed at feedURL, retrying
// transport errors, 429 (honoring Retry-After) and 5xx up to
// cfg.MaxRetries times with exponential backoff. The response body is
// bounded by cfg.ResponseLimitBytes before parsing. A URL refused by the
// host policy (see Config.AllowPrivateHosts) fails at once, without
// retries, with an error matching netguard.ErrDisallowedURL. Errors never
// carry the feed URL's query values or credentials.
func Fetch(ctx context.Context, cfg Config, feedURL string) (*gofeed.Feed, error) {
	policy := netguard.PolicyFor(cfg.AllowPrivateHosts)
	if _, err := netguard.ValidateURL(feedURL, policy); err != nil {
		return nil, fmt.Errorf("rssclient: %w", err)
	}
	client := newHTTPClient(cfg)

	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		feed, retryDelay, err := doFetch(ctx, client, cfg, feedURL)
		if err == nil {
			return feed, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("rssclient: %w", ctx.Err())
		}
		lastErr = err
		if attempt == cfg.MaxRetries || errors.Is(err, netguard.ErrDisallowedURL) {
			break
		}
		delay := retryDelay
		if delay == 0 {
			delay = computeBackoff(cfg.BackoffBase, cfg.BackoffMax, attempt)
		}
		if sleepErr := sleepFn(ctx, delay); sleepErr != nil {
			return nil, fmt.Errorf("rssclient: %w", sleepErr)
		}
	}
	return nil, fmt.Errorf("rssclient: fetch %s: %w", netguard.RedactURL(feedURL), lastErr)
}

// doFetch performs a single HTTP attempt. The returned duration is a
// server-requested retry delay (from Retry-After on 429), zero otherwise.
func doFetch(ctx context.Context, client *http.Client, cfg Config, feedURL string) (*gofeed.Feed, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", netguard.RedactError(err))
	}
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", netguard.RedactError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		delay, _ := retryAfter(resp.Header)
		return nil, delay, fmt.Errorf("HTTP 429")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := netguard.ReadBody(resp.Body, cfg.limit())
	if errors.Is(err, netguard.ErrBodyTooLarge) {
		return nil, 0, err
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}

	feed, err := ParseBytes(data, cfg.limit())
	if err != nil {
		return nil, 0, err
	}
	return feed, 0, nil
}

// ParseBytes parses a bounded RSS/Atom response body. It is exported so
// callers that obtained feed XML through another transport, such as the
// Scraping Service fallback, use the same parser and size guard. A
// non-positive responseLimitBytes means netguard.FeedBodyLimit.
func ParseBytes(data []byte, responseLimitBytes int64) (*gofeed.Feed, error) {
	if responseLimitBytes <= 0 {
		responseLimitBytes = netguard.FeedBodyLimit
	}
	if int64(len(data)) > responseLimitBytes {
		return nil, netguard.BodyTooLarge(responseLimitBytes)
	}
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	if err := rejectBridgeErrorFeed(feed); err != nil {
		return nil, err
	}
	return feed, nil
}

func rejectBridgeErrorFeed(feed *gofeed.Feed) error {
	for _, item := range feed.Items {
		title := strings.ToLower(strings.TrimSpace(item.Title))
		description := strings.ToLower(strings.TrimSpace(item.Description))
		content := strings.ToLower(strings.TrimSpace(item.Content))
		if strings.HasPrefix(title, "bridge returned error") {
			return fmt.Errorf("rss bridge error item: %s", strings.TrimSpace(item.Title))
		}
		if strings.Contains(description, "rss-bridge tried to fetch a website") ||
			strings.Contains(content, "rss-bridge tried to fetch a website") {
			return fmt.Errorf("rss bridge error item")
		}
	}
	return nil
}

// computeBackoff returns base * 2^attempt, capped at max.
func computeBackoff(base, max time.Duration, attempt int) time.Duration {
	if attempt > 62 {
		return max
	}
	d := base << attempt
	if d <= 0 || d > max {
		return max
	}
	return d
}

// retryAfter reads the Retry-After response header in its seconds form.
func retryAfter(h http.Header) (time.Duration, bool) {
	v := h.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// sleepFn is the retry-wait seam; tests replace it (export_test.go) to assert
// requested delays without spending wall-clock time.
var sleepFn = sleep

// sleep waits for d, or returns ctx.Err() if ctx is done first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
