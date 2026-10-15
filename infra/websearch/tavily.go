// infra/websearch/tavily.go
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const defaultTavilyURL = "https://api.tavily.com/search"

type tavilyClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

var _ appout.WebSearcher = (*tavilyClient)(nil)

// NewTavily builds a ports/out.WebSearcher for the Tavily search API using
// the "basic" depth (1 credit per search). Quota and rate-limit responses
// are reported as appout.ErrSearchProviderUnavailable; an invalid key is a
// plain error so it is never mistaken for a transient outage.
func NewTavily(cfg SearXNGConfig) appout.WebSearcher {
	base := cfg.BaseURL
	if base == "" {
		base = defaultTavilyURL
	}
	return &tavilyClient{
		httpClient: newHTTPClient(cfg.Timeout),
		baseURL:    base,
		apiKey:     cfg.APIKey,
	}
}

type tavilyRequest struct {
	Query          string   `json:"query"`
	SearchDepth    string   `json:"search_depth"`
	MaxResults     int      `json:"max_results"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	IncludeAnswer  bool     `json:"include_answer"`
}

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// Search implements ports/out.WebSearcher.
func (c *tavilyClient) Search(ctx context.Context, query string, opts appout.SearchOptions) ([]weblookup.SearchResult, error) {
	body, err := json.Marshal(tavilyRequest{Query: query, SearchDepth: "basic", MaxResults: opts.MaxResults, IncludeDomains: opts.TrustedDomains})
	if err != nil {
		return nil, fmt.Errorf("websearch: tavily encode request: %w", err)
	}
	req, err := buildRequest(ctx, http.MethodPost, c.baseURL, c.apiKey, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("websearch: tavily build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websearch: tavily request failed: %w", netguard.RedactError(err, c.apiKey))
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == 432, resp.StatusCode == 433:
		return nil, fmt.Errorf("websearch: tavily HTTP %d (rate or plan limit): %w", resp.StatusCode, appout.ErrSearchProviderUnavailable)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("websearch: tavily returned HTTP %d", resp.StatusCode)
	}
	data, err := readBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("websearch: tavily %w", err)
	}
	var parsed tavilyResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("websearch: tavily decode response: %w", err)
	}
	results := make([]weblookup.SearchResult, 0, opts.MaxResults)
	for _, r := range parsed.Results {
		if len(results) >= opts.MaxResults {
			break
		}
		if sr, ok := toSearchResult(searxngResult{Title: r.Title, URL: r.URL, Content: r.Content}, opts.TrustedDomains); ok {
			results = append(results, sr)
		}
	}
	return results, nil
}
