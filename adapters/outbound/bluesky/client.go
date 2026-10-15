// Package bluesky fetches public Bluesky accounts and curated feeds via
// the AT Protocol's public XRPC endpoints — no authentication, and no
// bluesky-social/indigo dependency. client.go is the HTTP
// client; fetcher.go (same package) parses its responses into
// article.Article.
package bluesky

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
)

const (
	defaultHost       = "https://public.api.bsky.app"
	maxErrorBodyBytes = 4096
	defaultTimeout    = 30 * time.Second
)

// Client is a minimal HTTP client against Bluesky's public XRPC endpoints.
type Client struct {
	Host       string
	httpClient *http.Client
	limitBytes int64
}

// NewClient builds a Client pointed at the real public Bluesky API. A
// non-positive timeout means 30s and a non-positive limitBytes means
// netguard.JSONBodyLimit. Redirects follow the netguard policy and the
// transport refuses link-local destinations; Host is not operator
// configuration (only tests override it), so loopback stays reachable.
func NewClient(timeout time.Duration, limitBytes int64) *Client {
	tr := netguard.NewTransport(netguard.AllowPrivate)
	tr.Proxy = nil
	tr.MaxIdleConnsPerHost = 4
	tr.IdleConnTimeout = 90 * time.Second
	return &Client{
		Host: defaultHost,
		httpClient: &http.Client{
			Timeout:       netguard.TimeoutOr(timeout, defaultTimeout),
			Transport:     tr,
			CheckRedirect: netguard.CheckRedirect,
		},
		limitBytes: limitBytes,
	}
}

// feedResponse is the shared shape of getAuthorFeed/getFeed responses —
// only the fields this bot needs.
type feedResponse struct {
	Feed []feedViewPost `json:"feed"`
}

type apiErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type feedViewPost struct {
	Post postView `json:"post"`
}

type postView struct {
	URI    string     `json:"uri"`
	Author authorView `json:"author"`
	Record postRecord `json:"record"`
}

type authorView struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
}

type postRecord struct {
	Text      string       `json:"text"`
	CreatedAt string       `json:"createdAt"`
	Facets    []facet      `json:"facets"`
	Embed     *recordEmbed `json:"embed"`
}

type facet struct {
	Features []facetFeature `json:"features"`
}

type facetFeature struct {
	Type string `json:"$type"`
	URI  string `json:"uri"`
}

type recordEmbed struct {
	Type     string         `json:"$type"`
	External *embedExternal `json:"external"`
}

type embedExternal struct {
	URI string `json:"uri"`
}

// GetAuthorFeed calls app.bsky.feed.getAuthorFeed for actor (a handle),
// excluding replies, returning up to limit posts.
func (c *Client) GetAuthorFeed(ctx context.Context, actor string, limit int) ([]feedViewPost, error) {
	q := url.Values{"actor": {actor}, "filter": {"posts_no_replies"}, "limit": {strconv.Itoa(limit)}}
	return c.getFeed(ctx, "app.bsky.feed.getAuthorFeed", q)
}

// GetFeed calls app.bsky.feed.getFeed for a curated feed generator URI,
// returning up to limit posts.
func (c *Client) GetFeed(ctx context.Context, feedURI string, limit int) ([]feedViewPost, error) {
	q := url.Values{"feed": {feedURI}, "limit": {strconv.Itoa(limit)}}
	return c.getFeed(ctx, "app.bsky.feed.getFeed", q)
}

func (c *Client) getFeed(ctx context.Context, endpoint string, query url.Values) ([]feedViewPost, error) {
	reqURL := c.Host + "/xrpc/" + endpoint + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bluesky: build request: %w", netguard.RedactError(err))
	}
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bluesky: request failed: %w", netguard.RedactError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp, endpoint)
	}

	data, err := netguard.ReadBody(resp.Body, c.limitBytes)
	if errors.Is(err, netguard.ErrBodyTooLarge) {
		return nil, fmt.Errorf("bluesky: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("bluesky: read response: %w", err)
	}

	var body feedResponse
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("bluesky: decode response: %w", err)
	}
	return body.Feed, nil
}

func (c *Client) statusError(resp *http.Response, endpoint string) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return fmt.Errorf("bluesky: HTTP %d for %s: read error body: %w", resp.StatusCode, endpoint, err)
	}

	var body apiErrorResponse
	if err := json.Unmarshal(data, &body); err == nil {
		switch {
		case body.Message != "" && body.Error != "":
			return fmt.Errorf("bluesky: HTTP %d for %s: %s (%s)", resp.StatusCode, endpoint, body.Message, body.Error)
		case body.Message != "":
			return fmt.Errorf("bluesky: HTTP %d for %s: %s", resp.StatusCode, endpoint, body.Message)
		case body.Error != "":
			return fmt.Errorf("bluesky: HTTP %d for %s: %s", resp.StatusCode, endpoint, body.Error)
		}
	}

	return fmt.Errorf("bluesky: HTTP %d for %s", resp.StatusCode, endpoint)
}
