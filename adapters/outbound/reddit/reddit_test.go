// adapters/outbound/reddit/reddit_test.go
package reddit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/reddit"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
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

func TestFetch_ParsesAndMapsArticle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/r/sprisotopes/new/.rss" {
			t.Errorf("request path = %q, want /r/sprisotopes/new/.rss", r.URL.Path)
		}
		_, _ = w.Write([]byte(rssFeed(`<item><title>Game thread</title><link>https://reddit.com/r/sprisotopes/1</link><guid>1</guid></item>`)))
	}))
	defer server.Close()

	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "sprisotopes", Mode: "new", SourceType: "community"}}, testClientConfig(), noopLogger{})
	f.BaseURL = server.URL
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 || articles[0].Title != "Game thread" {
		t.Fatalf("articles = %+v", articles)
	}
	if articles[0].Source.Name != "r/sprisotopes" || articles[0].Source.Type != "reddit" {
		t.Errorf("Source = %+v", articles[0].Source)
	}
	if want := mustID(t, source.TypeReddit, "reddit-sprisotopes"); articles[0].Source.ID != want {
		t.Errorf("Source.ID = %q, want %q", articles[0].Source.ID, want)
	}
}

func TestFetch_StripsHTMLFromDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>Game thread</title><link>https://reddit.com/r/sprisotopes/1</link><guid>1</guid><description>&lt;a href="https://reddit.com" target="_blank" rel="nofollow"&gt;Full thread&lt;/a&gt;</description></item>`)))
	}))
	defer server.Close()

	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "sprisotopes", Mode: "new", SourceType: "community"}}, testClientConfig(), noopLogger{})
	f.BaseURL = server.URL
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("articles = %+v", articles)
	}
	if want := "Full thread"; articles[0].Summary != want {
		t.Errorf("Summary = %q, want %q (href/target/blank/nofollow must not leak)", articles[0].Summary, want)
	}
}

func TestFetch_DefaultsEmptyModeToNew(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/r/sprisotopes/new/.rss" {
			t.Errorf("request path = %q, want default mode 'new'", r.URL.Path)
		}
		_, _ = w.Write([]byte(rssFeed("")))
	}))
	defer server.Close()

	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "sprisotopes"}}, testClientConfig(), noopLogger{})
	f.BaseURL = server.URL
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
}

func TestFetch_DropsStaleItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>Old</title><link>https://reddit.com/1</link><guid>1</guid><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate></item>`)))
	}))
	defer server.Close()

	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "sprisotopes", MaxAge: 24 * time.Hour}}, testClientConfig(), noopLogger{})
	f.BaseURL = server.URL
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
			`<item><title>A</title><link>https://reddit.com/1</link><guid>1</guid></item>` +
				`<item><title>A again</title><link>https://reddit.com/1</link><guid>1</guid></item>`,
		)))
	}))
	defer server.Close()

	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "sprisotopes"}}, testClientConfig(), noopLogger{})
	f.BaseURL = server.URL
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Errorf("articles = %+v, want 1 (deduped)", articles)
	}
}

func TestFetch_OneFeedFailureDoesNotStopOthers(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "badsub"}, {Subreddit: "goodsub"}}, testClientConfig(), noopLogger{})
	f.BaseURL = bad.URL
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v, want nil (one feed's failure must not abort the run)", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none (both point at the failing server in this test)", articles)
	}
}

func TestFetch_ContextCancellationPropagates(t *testing.T) {
	f := reddit.NewFetcher(nil, testClientConfig(), noopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx); err == nil {
		t.Fatal("Fetch() error = nil, want context.Canceled")
	}
}
