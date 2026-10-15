package websearch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/websearch"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

var searchOpts = appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}}

func providers() []string { return []string{"searxng", "tavily"} }

func newSearcher(t *testing.T, provider string, cfg websearch.SearXNGConfig) appout.WebSearcher {
	t.Helper()
	s, err := websearch.New(provider, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSearchers_TimeOutOnHangingServer(t *testing.T) {
	for _, p := range providers() {
		s := newSearcher(t, p, websearch.SearXNGConfig{BaseURL: httpfault.Hanging(t).URL, APIKey: "k", Timeout: 100 * time.Millisecond})
		start := time.Now()
		if _, err := s.Search(context.Background(), "q", searchOpts); err == nil {
			t.Fatalf("%s: want timeout error", p)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("%s: call took %v", p, time.Since(start))
		}
	}
}

func TestSearchers_ZeroTimeoutGetsDefault(t *testing.T) {
	for _, p := range providers() {
		if got := websearch.ClientTimeoutForTest(newSearcher(t, p, websearch.SearXNGConfig{BaseURL: "http://localhost:8081/search"})); got <= 0 {
			t.Fatalf("%s: timeout = %v, want a positive default", p, got)
		}
	}
}

func TestSearchers_RejectOversizedBody(t *testing.T) {
	for _, p := range providers() {
		s := newSearcher(t, p, websearch.SearXNGConfig{BaseURL: httpfault.Oversized(t, 3<<20).URL, APIKey: "k", Timeout: 5 * time.Second})
		if _, err := s.Search(context.Background(), "q", searchOpts); !errors.Is(err, netguard.ErrBodyTooLarge) {
			t.Fatalf("%s: err = %v, want ErrBodyTooLarge", p, err)
		}
	}
}

func TestSearchers_ErrorsNeverContainToken(t *testing.T) {
	for _, p := range providers() {
		for _, base := range []string{
			httpfault.Closing(t).URL + "/search?token=" + httpfault.Secret,
			"http://example.com/%zz?token=" + httpfault.Secret,
			"ftp://search.example.com/?token=" + httpfault.Secret,
		} {
			s := newSearcher(t, p, websearch.SearXNGConfig{BaseURL: base, APIKey: httpfault.Secret, Timeout: 2 * time.Second})
			_, err := s.Search(context.Background(), "q", searchOpts)
			if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
				t.Fatalf("%s base %q: err = %v, must be non-nil and secret-free", p, base, err)
			}
		}
	}
}

func TestSearchers_NeverFollowRedirects(t *testing.T) {
	for _, p := range providers() {
		srv, hits := httpfault.RedirectLoop(t)
		s := newSearcher(t, p, websearch.SearXNGConfig{BaseURL: srv.URL, APIKey: "k", Timeout: 2 * time.Second})
		if _, err := s.Search(context.Background(), "q", searchOpts); err == nil {
			t.Fatalf("%s: want error", p)
		}
		if hits.Load() != 1 {
			t.Fatalf("%s: hits = %d, want 1 (no redirect followed)", p, hits.Load())
		}
	}
}

func TestSearchers_RefuseLinkLocalBaseURL(t *testing.T) {
	for _, p := range providers() {
		s := newSearcher(t, p, websearch.SearXNGConfig{BaseURL: "http://169.254.169.254/search", APIKey: "k", Timeout: time.Second})
		if _, err := s.Search(context.Background(), "q", searchOpts); !errors.Is(err, netguard.ErrDisallowedURL) {
			t.Fatalf("%s: err = %v, want ErrDisallowedURL", p, err)
		}
	}
}

func TestSearchers_SendNeutralUserAgent(t *testing.T) {
	for _, p := range providers() {
		var ua string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua = r.UserAgent()
			_, _ = w.Write([]byte(`{"results":[]}`))
		}))
		s := newSearcher(t, p, websearch.SearXNGConfig{BaseURL: srv.URL, APIKey: "k", Timeout: 2 * time.Second})
		if _, err := s.Search(context.Background(), "q", searchOpts); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		srv.Close()
		if ua != netguard.DefaultUserAgent {
			t.Fatalf("%s: User-Agent = %q", p, ua)
		}
	}
}
