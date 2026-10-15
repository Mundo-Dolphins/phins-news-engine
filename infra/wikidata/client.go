// Package wikidata is the read-only Wikidata adapter for
// ports/out.KnowledgeSource. It talks to the Wikibase Action
// API only (wbsearchentities / wbgetentities) — never WDQS/SPARQL — and is
// the only code that sees raw Wikidata JSON. It is offline-tooling-only:
// the service binary must never construct it.
package wikidata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ProviderName is the cache/provider label for this adapter.
const ProviderName = "wikidata"

const (
	defaultMaxResponseBytes = 16 << 20 // 16 MiB, decompressed
	maxRetryAfter           = 30 * time.Second
	defaultRetryBackoff     = 2 * time.Second
)

var propertyIDPattern = regexp.MustCompile(`^P[0-9]+$`)

// Config is the adapter's own configuration — plain Go types, built by
// the caller (the tools CLI) from config.WikidataConfig.
type Config struct {
	BaseURL   string
	Language  string
	UserAgent string
	Timeout   time.Duration
	// MaxCandidates caps Search results regardless of the caller's limit.
	MaxCandidates int
	// MaxConcurrency caps in-flight requests; zero means 1.
	MaxConcurrency int
	// Properties overrides the default statement allow-list.
	Properties []string
	// MaxResponseBytes bounds the decompressed body; zero means 16 MiB
	// (wbgetentities returns whole entity documents, far above the 1 MiB
	// used for smaller JSON APIs).
	MaxResponseBytes int64
	// AllowPrivateHosts lets BaseURL point at loopback or private-network
	// addresses (a local mirror or a test server). Default false: the
	// public Wikidata API is the expected endpoint, so loopback, RFC 1918,
	// CGNAT and link-local destinations are refused, both on the URL and
	// at dial time. When it is true, link-local addresses and the known
	// cloud metadata endpoints (netguard's deny list) are still refused,
	// and the environment proxy is honoured (see docs/SECURITY.md).
	AllowPrivateHosts bool
}

// Client implements ports/out.KnowledgeSource against the Wikibase Action
// API.
type Client struct {
	cfg        Config
	httpClient *http.Client
	sem        chan struct{}
	properties []string
	sleep      func(context.Context, time.Duration) error
	now        func() time.Time
}

var _ appout.KnowledgeSource = (*Client)(nil)

// New validates cfg and builds a Client. Redirects are never followed so
// a misconfigured base URL cannot bounce requests to another host.
func New(cfg Config) (*Client, error) {
	policy := netguard.PolicyFor(cfg.AllowPrivateHosts)
	switch {
	case cfg.BaseURL == "":
		return nil, errors.New("wikidata: base_url is required")
	case cfg.Language == "":
		return nil, errors.New("wikidata: language is required")
	case strings.TrimSpace(cfg.UserAgent) == "":
		return nil, errors.New("wikidata: user_agent is required")
	case cfg.Timeout <= 0:
		return nil, errors.New("wikidata: timeout must be positive")
	case cfg.MaxCandidates <= 0:
		return nil, errors.New("wikidata: max_candidates must be positive")
	case cfg.MaxConcurrency < 0:
		return nil, errors.New("wikidata: max_concurrency must not be negative")
	}
	if _, err := netguard.ValidateURL(cfg.BaseURL, policy); err != nil {
		return nil, fmt.Errorf("wikidata: base_url: %w", err)
	}
	props := cfg.Properties
	if len(props) == 0 {
		props = DefaultProperties
	}
	for _, p := range props {
		if !propertyIDPattern.MatchString(p) {
			return nil, fmt.Errorf("wikidata: invalid property id %q", p)
		}
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	concurrency := cfg.MaxConcurrency
	if concurrency == 0 {
		concurrency = 1
	}
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:       cfg.Timeout,
			Transport:     netguard.NewTransport(policy),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		sem:        make(chan struct{}, concurrency),
		properties: props,
		sleep:      sleepContext,
		now:        time.Now,
	}, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type apiError struct {
	Code string `json:"code"`
	Info string `json:"info"`
}

// check turns a Wikibase top-level error payload into a Go error. maxlag
// and ratelimited are transient; everything else is not.
func (e *apiError) check() error {
	if e == nil {
		return nil
	}
	if e.Code == "maxlag" || e.Code == "ratelimited" {
		return fmt.Errorf("wikidata: api error %q: %w", e.Code, appout.ErrTransient)
	}
	return fmt.Errorf("wikidata: api error %q: %s", e.Code, e.Info)
}

type searchResponse struct {
	Search []struct {
		ID          string   `json:"id"`
		Label       string   `json:"label"`
		Description string   `json:"description"`
		Aliases     []string `json:"aliases"`
	} `json:"search"`
	Error *apiError `json:"error"`
}

// Search implements ports/out.KnowledgeSource. Results keep Wikidata's
// order; the caller must not treat the first as authoritative.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]knowledge.Candidate, error) {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil, nil
	}
	if limit > c.cfg.MaxCandidates {
		limit = c.cfg.MaxCandidates
	}
	params := url.Values{}
	params.Set("action", "wbsearchentities")
	params.Set("format", "json")
	params.Set("search", query)
	params.Set("language", c.cfg.Language)
	params.Set("uselang", c.cfg.Language)
	params.Set("type", "item")
	params.Set("limit", strconv.Itoa(limit))

	body, err := c.do(ctx, params)
	if err != nil {
		return nil, err
	}
	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("wikidata: decode search response: %w", err)
	}
	if err := resp.Error.check(); err != nil {
		return nil, err
	}
	out := make([]knowledge.Candidate, 0, len(resp.Search))
	for _, r := range resp.Search {
		if r.ID == "" {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, knowledge.Candidate{ID: r.ID, Label: r.Label, Description: r.Description, Aliases: r.Aliases})
	}
	return out, nil
}

// do performs one GET against the Action API under the concurrency cap,
// with at most one retry for HTTP 429/5xx.
func (c *Client) do(ctx context.Context, params url.Values) ([]byte, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("wikidata: parse base_url: %w", netguard.RedactError(err))
	}
	q := u.Query()
	for k, vs := range params {
		q[k] = vs
	}
	u.RawQuery = q.Encode()
	endpoint := u.String()

	for attempt := 0; ; attempt++ {
		body, retryable, retryAfter, err := c.once(ctx, endpoint)
		if err == nil {
			return body, nil
		}
		if !retryable || attempt >= 1 {
			return nil, err
		}
		wait := retryAfter
		if wait <= 0 {
			wait = defaultRetryBackoff
		}
		if wait > maxRetryAfter {
			return nil, err
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			return nil, serr
		}
	}
}

// once issues a single request. retryable is true only for HTTP 429/5xx.
func (c *Client) once(ctx context.Context, endpoint string) (body []byte, retryable bool, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, 0, fmt.Errorf("wikidata: build request: %w", netguard.RedactError(err))
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	// Accept-Encoding is deliberately left to net/http, which negotiates
	// gzip and transparently decompresses.

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, 0, ctx.Err()
		}
		err = netguard.RedactError(err)
		if errors.Is(err, netguard.ErrDisallowedURL) {
			return nil, false, 0, fmt.Errorf("wikidata: request failed: %w", err)
		}
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, false, 0, fmt.Errorf("wikidata: request failed: %v: %w", err, appout.ErrTransient)
		}
		return nil, false, 0, fmt.Errorf("wikidata: request failed: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, true, c.parseRetryAfter(resp.Header.Get("Retry-After")), fmt.Errorf("wikidata: HTTP %d: %w", resp.StatusCode, appout.ErrTransient)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, false, 0, fmt.Errorf("wikidata: HTTP %d", resp.StatusCode)
	}

	data, err := netguard.ReadBody(resp.Body, c.cfg.MaxResponseBytes)
	if errors.Is(err, netguard.ErrBodyTooLarge) {
		return nil, false, 0, fmt.Errorf("wikidata: %w", err)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, 0, ctx.Err()
		}
		return nil, false, 0, fmt.Errorf("wikidata: read response: %v: %w", err, appout.ErrTransient)
	}
	return data, false, 0, nil
}

// parseRetryAfter understands both delta-seconds and HTTP-date forms; an
// absent, malformed or past value yields 0 (caller uses its own backoff).
func (c *Client) parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(c.now()); d > 0 {
			return d
		}
	}
	return 0
}
