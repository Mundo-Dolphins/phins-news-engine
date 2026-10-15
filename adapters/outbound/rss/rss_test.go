// adapters/outbound/rss/rss_test.go
package rss_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/rss"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)        {}
func (noopLogger) Info(string, ...any)         {}
func (noopLogger) Warn(string, ...any)         {}
func (noopLogger) Error(string, error, ...any) {}

func mustID(t *testing.T, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}

func testClientConfig() rssclient.Config {
	return rssclient.Config{Timeout: 2 * time.Second, MaxRetries: 0, BackoffBase: time.Millisecond, BackoffMax: time.Millisecond, ResponseLimitBytes: 1 << 20, AllowPrivateHosts: true}
}

func rssFeed(items string) string {
	return `<?xml version="1.0"?><rss version="2.0"><channel><title>F</title>` + items + `</channel></rss>`
}

type fakeScraper struct {
	result out.ScrapeResult
	err    error
	calls  int
	reqs   []out.ScrapeRequest
}

func (s *fakeScraper) Scrape(_ context.Context, req out.ScrapeRequest) (out.ScrapeResult, error) {
	s.calls++
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return out.ScrapeResult{}, s.err
	}
	return s.result, nil
}

func TestFetch_ParsesAndMapsArticle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>Isotopes sign new punter</title><link>https://example.com/1</link><guid>1</guid><pubDate>Mon, 02 Jan 2024 15:04:05 GMT</pubDate></item>`)))
	}))
	defer server.Close()

	f := rss.NewFetcher([]rss.FeedConfig{{Name: "Example Feed", URL: server.URL, SourceType: "hard_news"}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("articles = %+v, want 1", articles)
	}
	a := articles[0]
	if a.Title != "Isotopes sign new punter" || a.OriginalURL != "https://example.com/1" || a.ExternalID != "1" {
		t.Errorf("article = %+v", a)
	}
	if a.Source.Type != "rss" || a.Source.Name != "Example Feed" {
		t.Errorf("Source = %+v", a.Source)
	}
	if want := mustID(t, source.TypeRSS, "Example Feed"); a.Source.ID != want {
		t.Errorf("Source.ID = %q, want %q", a.Source.ID, want)
	}
	if a.PublishedAt.IsZero() {
		t.Error("PublishedAt not parsed")
	}
}

func TestFetch_DropsItemsMissingLinkAndGUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>No identity</title></item>`)))
	}))
	defer server.Close()

	f := rss.NewFetcher([]rss.FeedConfig{{Name: "F", URL: server.URL}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none", articles)
	}
}

func TestFetch_AppliesBlockedAndRequiredKeywords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(
			`<item><title>Isotopes injury report</title><link>https://example.com/1</link><guid>1</guid></item>` +
				`<item><title>Isotopes news roundup</title><link>https://example.com/2</link><guid>2</guid></item>`,
		)))
	}))
	defer server.Close()

	f := rss.NewFetcher([]rss.FeedConfig{{
		Name: "F", URL: server.URL,
		BlockedKeywords:  []string{"injury"},
		RequiredKeywords: []string{"roundup"},
	}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 || articles[0].ExternalID != "2" {
		t.Fatalf("articles = %+v, want only the roundup item", articles)
	}
}

func TestFetch_DropsStaleItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>Old news</title><link>https://example.com/1</link><guid>1</guid><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate></item>`)))
	}))
	defer server.Close()

	f := rss.NewFetcher([]rss.FeedConfig{{Name: "F", URL: server.URL, MaxAge: 24 * time.Hour}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none (stale)", articles)
	}
}

func TestFetch_DedupsWithinCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(
			`<item><title>A</title><link>https://example.com/1</link><guid>1</guid></item>` +
				`<item><title>A again</title><link>https://example.com/1</link><guid>1</guid></item>`,
		)))
	}))
	defer server.Close()

	f := rss.NewFetcher([]rss.FeedConfig{{Name: "F", URL: server.URL}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Errorf("articles = %+v, want 1 (deduped)", articles)
	}
}

func TestFetch_OneFeedFailureDoesNotStopOthers(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>Good</title><link>https://example.com/1</link><guid>1</guid></item>`)))
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	f := rss.NewFetcher([]rss.FeedConfig{{Name: "Bad", URL: bad.URL}, {Name: "Good", URL: good.URL}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v, want nil (one feed's failure must not abort the run)", err)
	}
	if len(articles) != 1 || articles[0].Source.Name != "Good" {
		t.Fatalf("articles = %+v, want 1 from Good", articles)
	}
}

func TestFetch_FallsBackToScrapingServiceWhenDirectFeedFetchFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	scraper := &fakeScraper{result: out.ScrapeResult{
		HTML: rssFeed(`<item><title>Fallback item</title><link>https://example.com/fallback</link><guid>fallback</guid></item>`),
	}}

	f := rss.NewFetcherWithScraper([]rss.FeedConfig{{Name: "Blocked", URL: server.URL}}, testClientConfig(), scraper, noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if scraper.calls != 1 {
		t.Fatalf("scraper.calls = %d, want 1", scraper.calls)
	}
	if len(scraper.reqs) != 1 || scraper.reqs[0].URL != server.URL || scraper.reqs[0].Mode != out.ScrapeModeAuto {
		t.Fatalf("scraper.reqs = %+v, want URL=%s with auto mode", scraper.reqs, server.URL)
	}
	if len(articles) != 1 || articles[0].ExternalID != "fallback" {
		t.Fatalf("articles = %+v, want fallback item", articles)
	}
}

func TestFetch_ContextCancellationPropagates(t *testing.T) {
	f := rss.NewFetcher(nil, testClientConfig(), noopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx); err == nil {
		t.Fatal("Fetch() error = nil, want context.Canceled")
	}
}
