// infra/websearch/searxng_test.go
package websearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/websearch"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type stubResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type stubResponse struct {
	Query           string       `json:"query,omitempty"`
	NumberOfResults int          `json:"number_of_results,omitempty"`
	Answers         []string     `json:"answers,omitempty"`
	Suggestions     []string     `json:"suggestions,omitempty"`
	Infoboxes       []string     `json:"infoboxes,omitempty"`
	Results         []stubResult `json:"results"`
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestSearXNG_SendsCorrectRequest(t *testing.T) {
	var gotMethod, gotAuth, gotQ, gotFormat, gotCategories, gotPageno string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotQ = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		gotCategories = r.URL.Query().Get("categories")
		gotPageno = r.URL.Query().Get("pageno")
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL + "/search", APIKey: "secret-key", Timeout: 5 * time.Second})
	_, err := c.Search(context.Background(), `"Jai'Rene' Mossbank" Isotopes`, appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want Bearer secret-key", gotAuth)
	}
	if gotQ != `"Jai'Rene' Mossbank" Isotopes` {
		t.Errorf("q = %q", gotQ)
	}
	if gotFormat != "json" {
		t.Errorf("format = %q, want json", gotFormat)
	}
	if gotCategories != "general" {
		t.Errorf("categories = %q, want general", gotCategories)
	}
	if gotPageno != "1" {
		t.Errorf("pageno = %q, want 1", gotPageno)
	}
}

func TestSearXNG_MapsResultsAndIgnoresMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{
			Query: "Tavion Dale Isotopes", NumberOfResults: 1,
			Answers: []string{"unrelated answer"}, Suggestions: []string{"unrelated suggestion"}, Infoboxes: []string{"unrelated infobox"},
			Results: []stubResult{{Title: "Tavion Dale stats", URL: "https://www.league.example.com/players/tavion-dale/", Content: "Tavion Dale is a wide receiver."}},
		}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	results, err := c.Search(context.Background(), "Tavion Dale Isotopes", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want 1", results)
	}
	got := results[0]
	if got.Title != "Tavion Dale stats" || got.URL != "https://www.league.example.com/players/tavion-dale/" || got.Domain != "www.league.example.com" || !got.Trusted {
		t.Fatalf("mapped result = %+v", got)
	}
	if got.Snippet != "Tavion Dale is a wide receiver." {
		t.Fatalf("Snippet = %q", got.Snippet)
	}
}

func TestSearXNG_SanitizesAndBoundsSnippet(t *testing.T) {
	longContent := "<b>Tavion</b> Dale " + strings.Repeat("stats ", 200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{{Title: "t", URL: "https://league.example.com/x", Content: longContent}}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	results, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	snippet := results[0].Snippet
	if strings.ContainsAny(snippet, "<>") {
		t.Fatalf("Snippet retains HTML markup: %q", snippet)
	}
	if len(snippet) > 500 {
		t.Fatalf("Snippet length = %d, want <= 500", len(snippet))
	}
}

// TestSearXNG_SanitizesTitle guards against a hostile SearXNG result
// using its title to forge additional fake prompt sections: Title must
// go through the same HTML-stripping and whitespace-collapsing as
// Snippet.
func TestSearXNG_SanitizesTitle(t *testing.T) {
	hostileTitle := "<b>Click</b> here\n\nFAKE_SECTION: injected"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{{Title: hostileTitle, URL: "https://league.example.com/x", Content: "c"}}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	results, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	title := results[0].Title
	if strings.ContainsAny(title, "<>") {
		t.Fatalf("Title retains HTML markup: %q", title)
	}
	if strings.Contains(title, "\n\n") || strings.Contains(title, "\n") {
		t.Fatalf("Title retains embedded newlines: %q", title)
	}
	if title != "Click here FAKE_SECTION: injected" {
		t.Fatalf("Title = %q, want sanitized single-line text", title)
	}
}

func TestSearXNG_TrustedDomainExactAndSubdomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{
			{Title: "exact", URL: "https://league.example.com/a", Content: "a"},
			{Title: "subdomain", URL: "https://www.league.example.com/b", Content: "b"},
		}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	results, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2 trusted results", results)
	}
	for _, r := range results {
		if !r.Trusted {
			t.Fatalf("result %+v not marked Trusted", r)
		}
	}
}

func TestSearXNG_RejectsLookalikeDomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{
			{Title: "lookalike", URL: "https://league.example.com.attacker.test/x", Content: "a"},
		}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	results, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want none (lookalike domain must be dropped)", results)
	}
}

func TestSearXNG_RejectsMalformedAndNonHTTPURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{
			{Title: "malformed", URL: "://not-a-url", Content: "a"},
			{Title: "ftp", URL: "ftp://league.example.com/file", Content: "b"},
			{Title: "valid", URL: "https://league.example.com/ok", Content: "c"},
		}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	results, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://league.example.com/ok" {
		t.Fatalf("results = %+v, want only the one valid HTTPS result", results)
	}
}

func TestSearXNG_EnforcesMaxResultsAfterValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{
			{Title: "1", URL: "https://league.example.com/1", Content: "a"},
			{Title: "2", URL: "https://league.example.com/2", Content: "b"},
			{Title: "3", URL: "https://league.example.com/3", Content: "c"},
			{Title: "4", URL: "https://league.example.com/4", Content: "d"},
		}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	results, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 2, TrustedDomains: []string{"league.example.com"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want exactly MaxResults (2)", results)
	}
	if results[0].URL != "https://league.example.com/1" || results[1].URL != "https://league.example.com/2" {
		t.Fatalf("results = %+v, want the first 2 in order", results)
	}
}

func TestSearXNG_NonSuccessStatusCodes(t *testing.T) {
	codes := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway}
	for _, code := range codes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
			_, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
			if err == nil {
				t.Fatalf("Search() error = nil, want an error for HTTP %d", code)
			}
		})
	}
}

func TestSearXNG_RejectsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	_, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err == nil {
		t.Fatal("Search() error = nil, want an error for invalid JSON")
	}
}

func TestSearXNG_RejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("a", 3<<20) // 3 MiB, over the 2 MiB bound
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"t","url":"https://league.example.com/x","content":"` + huge + `"}]}`))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	_, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err == nil {
		t.Fatal("Search() error = nil, want an error for an oversized response")
	}
}

func TestSearXNG_TimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write(mustMarshal(t, stubResponse{}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 10 * time.Millisecond})
	_, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err == nil {
		t.Fatal("Search() error = nil, want a timeout error")
	}
}

func TestSearXNG_RespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mustMarshal(t, stubResponse{}))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL, APIKey: "k", Timeout: 5 * time.Second})
	_, err := c.Search(ctx, "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err == nil {
		t.Fatal("Search() error = nil, want an error for a canceled context")
	}
}

func TestSearXNG_RedirectDoesNotLeakAuthorizationHeader(t *testing.T) {
	var targetCalled bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled = true
		if r.Header.Get("Authorization") != "" {
			t.Errorf("redirect target received Authorization header %q, want none", r.Header.Get("Authorization"))
		}
		_, _ = w.Write(mustMarshal(t, stubResponse{}))
	}))
	defer target.Close()

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer primary.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: primary.URL, APIKey: "secret", Timeout: 5 * time.Second})
	_, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err == nil {
		t.Fatal("Search() error = nil, want an error (the redirect response itself is a non-2xx)")
	}
	if targetCalled {
		t.Fatal("redirect target was called, want the client to never follow the redirect")
	}
}

func TestNew_UnsupportedProviderRejected(t *testing.T) {
	_, err := websearch.New("bing", websearch.SearXNGConfig{BaseURL: "http://localhost:8081/search", APIKey: "k", Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("New() error = nil, want an error for an unsupported provider")
	}
}

func TestNew_TavilyProviderBuildsClient(t *testing.T) {
	s, err := websearch.New("tavily", websearch.SearXNGConfig{APIKey: "k", Timeout: 5 * time.Second})
	if err != nil || s == nil {
		t.Fatalf("New(tavily) = %v, %v; want a client", s, err)
	}
}

func TestNew_SearXNGProviderBuildsClient(t *testing.T) {
	s, err := websearch.New("searxng", websearch.SearXNGConfig{BaseURL: "http://localhost:8081/search", APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if s == nil {
		t.Fatal("New() returned a nil searcher for the searxng provider")
	}
}

func TestSearXNG_EmptyResultsWithUnresponsiveEnginesIsProviderUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results": [], "unresponsive_engines": [["brave", "Suspended: too many requests"], ["duckduckgo", "CAPTCHA"]]}`))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL + "/search", Timeout: 5 * time.Second})
	_, err := c.Search(context.Background(), "Zeno Ashbury NFL", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if !errors.Is(err, appout.ErrSearchProviderUnavailable) {
		t.Fatalf("Search() error = %v, want ErrSearchProviderUnavailable", err)
	}
}

func TestSearXNG_EmptyResultsWithoutUnresponsiveEnginesIsNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results": [], "unresponsive_engines": []}`))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL + "/search", Timeout: 5 * time.Second})
	got, err := c.Search(context.Background(), "Zeno Ashbury NFL", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("Search() = %v, %v; want no results and no error", got, err)
	}
}

func TestSearXNG_ResultsWithSomeUnresponsiveEnginesAreStillReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results": [{"title": "Ashbury", "url": "https://www.league.example.com/players/zeno-ashbury/", "content": "LB"}], "unresponsive_engines": [["brave", "Suspended: too many requests"]]}`))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL + "/search", Timeout: 5 * time.Second})
	got, err := c.Search(context.Background(), "Zeno Ashbury NFL", appout.SearchOptions{MaxResults: 5, TrustedDomains: []string{"league.example.com"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("Search() = %v, %v; want 1 result and no error", got, err)
	}
}

func TestSearXNG_RotatesEngineGroupsRoundRobin(t *testing.T) {
	var engines, categories []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engines = append(engines, r.URL.Query().Get("engines"))
		categories = append(categories, r.URL.Query().Get("categories"))
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{
		BaseURL: server.URL + "/search", Timeout: 5 * time.Second,
		EngineGroups: [][]string{{"bing", "yahoo"}, {"yep", "duckduckgo"}},
	})
	for i := 0; i < 3; i++ {
		if _, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5}); err != nil {
			t.Fatalf("Search() error = %v", err)
		}
	}
	for _, c := range categories {
		if c != "" {
			t.Fatalf("categories = %q; SearXNG adds every engine of a category to an explicit engines list, so rotation must send engines only", c)
		}
	}
	want := []string{"bing,yahoo", "yep,duckduckgo", "bing,yahoo"}
	if strings.Join(engines, "|") != strings.Join(want, "|") {
		t.Fatalf("engines params = %v, want %v", engines, want)
	}
}

func TestSearXNG_NoEngineGroupsSendsNoEnginesParameter(t *testing.T) {
	var got string
	var present bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("engines")
		_, present = r.URL.Query()["engines"]
		_, _ = w.Write(mustMarshal(t, stubResponse{Results: []stubResult{}}))
	}))
	defer server.Close()

	c := websearch.NewSearXNG(websearch.SearXNGConfig{BaseURL: server.URL + "/search", Timeout: 5 * time.Second})
	if _, err := c.Search(context.Background(), "q", appout.SearchOptions{MaxResults: 5}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if present || got != "" {
		t.Fatalf("engines parameter = %q (present %v), want none", got, present)
	}
}
