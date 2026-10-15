// infra/websearch/tavily_test.go
package websearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/websearch"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func tavilyClient(url string) appout.WebSearcher {
	return websearch.NewTavily(websearch.SearXNGConfig{BaseURL: url, APIKey: "tvly-test", Timeout: 5 * time.Second})
}

func TestTavily_SendsBasicDomainRestrictedPostAndMapsTrustedResults(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Zeno Ashbury","url":"https://www.sportsnet.example.com/nfl/player/_/id/1000001/zeno-ashbury","content":"<b>LB</b> Herons"},
			{"title":"Spam","url":"https://evil.example.com/x","content":"x"}]}`))
	}))
	defer srv.Close()

	res, err := tavilyClient(srv.URL).Search(context.Background(), "Zeno Ashbury NFL team", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"sportsnet.example.com", "league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if auth != "Bearer tvly-test" {
		t.Errorf("Authorization = %q", auth)
	}
	if got["search_depth"] != "basic" || got["query"] != "Zeno Ashbury NFL team" || got["include_answer"] != false {
		t.Errorf("body = %v", got)
	}
	if d, _ := got["include_domains"].([]any); len(d) != 2 {
		t.Errorf("include_domains = %v, want the 2 trusted domains", got["include_domains"])
	}
	if len(res) != 1 || res[0].Domain != "www.sportsnet.example.com" || !res[0].Trusted || res[0].Snippet != "LB Herons" {
		t.Fatalf("results = %+v, want 1 sanitized trusted result", res)
	}
}

func TestTavily_StatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status      int
		unavailable bool
	}{{http.StatusTooManyRequests, true}, {432, true}, {433, true}, {http.StatusUnauthorized, false}, {http.StatusInternalServerError, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status) }))
		_, err := tavilyClient(srv.URL).Search(context.Background(), "q", appout.SearchOptions{MaxResults: 1, TrustedDomains: []string{"league.example.com"}})
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: error = nil", tc.status)
		}
		if got := errors.Is(err, appout.ErrSearchProviderUnavailable); got != tc.unavailable {
			t.Errorf("status %d: unavailable = %v, want %v (err %v)", tc.status, got, tc.unavailable, err)
		}
	}
}
