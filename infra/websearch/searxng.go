// infra/websearch/searxng.go
package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// maxResponseBytes bounds how much of a SearXNG response body is ever read
// into memory: an oversized response is rejected as a
// provider error rather than exhausting memory.
const maxResponseBytes = 2 << 20 // 2 MiB

// defaultTimeout applies when a provider config has no positive Timeout.
const defaultTimeout = 10 * time.Second

// newHTTPClient builds the client both providers use: a timeout that is
// never zero, redirects never followed (so the Authorization header can
// never be forwarded to another host), and a transport that refuses
// link-local destinations at dial time. The base URL is host-operator
// configuration, typically a SearXNG instance on the LAN or loopback, so
// private hosts are allowed.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   netguard.TimeoutOr(timeout, defaultTimeout),
		Transport: netguard.NewTransport(netguard.AllowPrivate),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// buildRequest validates baseURL and returns the authenticated request;
// every error is free of the API key and of URL query values.
func buildRequest(ctx context.Context, method, rawURL, apiKey string, body io.Reader) (*http.Request, error) {
	if _, err := netguard.ValidateURL(rawURL, netguard.AllowPrivate); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, netguard.RedactError(err, apiKey)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)
	return req, nil
}

// readBody reads at most maxResponseBytes; an oversized body matches
// netguard.ErrBodyTooLarge.
func readBody(r io.Reader) ([]byte, error) {
	data, err := netguard.ReadBody(r, maxResponseBytes)
	if err != nil && !errors.Is(err, netguard.ErrBodyTooLarge) {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return data, err
}

// maxSnippetLength bounds the plain-text snippet forwarded to the local
// LLM. SearXNG's `content` field is documented as plain
// text, but this is enforced locally rather than trusted from the
// provider.
const maxSnippetLength = 500

// SearXNGConfig is searxngClient's own adapter-level configuration — plain
// Go types, no YAML tags, built by the caller (the tools CLI)
// from config.WebLookupConfig, the same pattern a scraper adapter's config
// uses.
type SearXNGConfig struct {
	// BaseURL is the SearXNG search endpoint, e.g.
	// "http://localhost:8081/search".
	BaseURL string
	// APIKey is sent as a Bearer token. Never logged.
	APIKey  string
	Timeout time.Duration
	// EngineGroups, when non-empty, makes consecutive searches ask different
	// engine sets round-robin (sent as the SearXNG `engines` parameter), so
	// each upstream engine sees only a fraction of the traffic.
	EngineGroups [][]string
}

// searxngClient is the ports/out.WebSearcher adapter for a private
// SearXNG instance.
type searxngClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	groups     [][]string
	calls      atomic.Uint64
}

var _ appout.WebSearcher = (*searxngClient)(nil)

// NewSearXNG builds a ports/out.WebSearcher backed by a SearXNG
// instance's JSON API. It never follows redirects — a redirect response
// is instead treated as the final response (and rejected by Search's
// non-2xx check) — so the Authorization header can never be forwarded to
// a different host.
func NewSearXNG(cfg SearXNGConfig) appout.WebSearcher {
	return &searxngClient{
		httpClient: newHTTPClient(cfg.Timeout),
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		groups:     cfg.EngineGroups,
	}
}

// New builds the ports/out.WebSearcher for the configured provider.
// "searxng" is the only supported value; any other provider is rejected
// with a clear configuration error rather than silently degrading (issue
// #247).
func New(provider string, cfg SearXNGConfig) (appout.WebSearcher, error) {
	switch provider {
	case "searxng":
		return NewSearXNG(cfg), nil
	case "tavily":
		return NewTavily(cfg), nil
	default:
		return nil, fmt.Errorf("websearch: unsupported provider %q (supported: searxng, tavily)", provider)
	}
}

// searxngResponse models only the fields the local LLM needs. Answers,
// suggestions, infoboxes, engine metadata, thumbnails and every other
// SearXNG field are deliberately not modeled — encoding/json ignores
// unknown fields, which is exactly the "ignore metadata the LLM doesn't
// need" behavior callers expect.
type searxngResponse struct {
	Results []searxngResult `json:"results"`
	// UnresponsiveEngines lists [engine, reason] pairs for upstream engines
	// that failed (suspended, CAPTCHA, timeout). Used only to tell an outage
	// apart from a genuinely empty answer.
	UnresponsiveEngines [][]string `json:"unresponsive_engines"`
}

type searxngResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Search implements ports/out.WebSearcher. query has already passed
// weblookup.ValidateQuery before this is called. Search never trusts the
// provider's URL/domain data on its own — every result is re-validated
// against opts.TrustedDomains locally, and MaxResults is enforced only after that local
// validation, since SearXNG exposes no provider-independent max_results
// parameter.
func (c *searxngClient) Search(ctx context.Context, query string, opts appout.SearchOptions) ([]weblookup.SearchResult, error) {
	endpoint, err := netguard.ValidateURL(c.baseURL, netguard.AllowPrivate)
	if err != nil {
		return nil, fmt.Errorf("websearch: invalid base_url: %w", err)
	}
	q := endpoint.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if len(c.groups) == 0 {
		// With an explicit engines list SearXNG still adds every engine of the
		// requested categories, which would defeat engine rotation.
		q.Set("categories", "general")
	}
	q.Set("pageno", "1")
	if len(c.groups) > 0 {
		n := c.calls.Add(1) - 1
		q.Set("engines", strings.Join(c.groups[n%uint64(len(c.groups))], ","))
	}
	endpoint.RawQuery = q.Encode()

	req, err := buildRequest(ctx, http.MethodGet, endpoint.String(), c.apiKey, nil)
	if err != nil {
		return nil, fmt.Errorf("websearch: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websearch: request failed: %w", netguard.RedactError(err, c.apiKey))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("websearch: searxng returned HTTP %d", resp.StatusCode)
	}

	data, err := readBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("websearch: %w", err)
	}

	var body searxngResponse
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("websearch: decode response: %w", err)
	}

	// SearXNG answers HTTP 200 with no results when its engines are
	// rate-limited, which is indistinguishable from "nothing found" unless
	// the unresponsive engines are checked. Report it as an outage so the
	// caller neither records "no evidence" nor caches the empty answer.
	if len(body.Results) == 0 && len(body.UnresponsiveEngines) > 0 {
		return nil, fmt.Errorf("websearch: searxng returned no results and %d unresponsive engines (%s): %w",
			len(body.UnresponsiveEngines), describeEngines(body.UnresponsiveEngines), appout.ErrSearchProviderUnavailable)
	}

	results := make([]weblookup.SearchResult, 0, opts.MaxResults)
	for _, r := range body.Results {
		if len(results) >= opts.MaxResults {
			break
		}
		sr, ok := toSearchResult(r, opts.TrustedDomains)
		if !ok {
			continue
		}
		results = append(results, sr)
	}
	return results, nil
}

// toSearchResult validates and maps one SearXNG result. It parses the
// URL, requires HTTP/HTTPS, normalizes the hostname, and requires an
// exact or subdomain trusted-domain match — a malformed URL, a non-HTTP
// scheme, or an untrusted/lookalike domain is dropped (ok == false),
// never marked Trusted.
func toSearchResult(r searxngResult, trustedDomains []string) (weblookup.SearchResult, bool) {
	parsed, err := url.Parse(r.URL)
	if err != nil {
		return weblookup.SearchResult{}, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return weblookup.SearchResult{}, false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return weblookup.SearchResult{}, false
	}
	if !weblookup.IsTrustedDomain(host, trustedDomains) {
		return weblookup.SearchResult{}, false
	}
	return weblookup.SearchResult{
		Title:   sanitizeSnippet(r.Title),
		URL:     r.URL,
		Snippet: sanitizeSnippet(r.Content),
		Domain:  host,
		Trusted: true,
	}, true
}

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// sanitizeSnippet strips HTML markup, collapses whitespace (including
// embedded newlines, so a hostile title cannot forge additional prompt
// sections) and bounds the result to maxSnippetLength. Used
// for both Title and Snippet.
func sanitizeSnippet(raw string) string {
	stripped := htmlTagPattern.ReplaceAllString(raw, "")
	stripped = strings.Join(strings.Fields(stripped), " ")
	if len(stripped) > maxSnippetLength {
		stripped = strings.TrimSpace(stripped[:maxSnippetLength])
	}
	return stripped
}

// describeEngines renders "engine: reason" pairs for the error message.
func describeEngines(engines [][]string) string {
	parts := make([]string, 0, len(engines))
	for _, e := range engines {
		parts = append(parts, strings.Join(e, ": "))
	}
	return strings.Join(parts, "; ")
}
