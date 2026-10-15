// adapters/outbound/youtube/youtube_test.go
package youtube_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/youtube"
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

func TestFetch_StripsHTMLFromDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>Isotopes highlights</title><link>https://www.youtube.com/watch?v=abc</link><guid>abc</guid><description>&lt;a href="https://youtube.com" target="_blank" rel="nofollow"&gt;Watch now&lt;/a&gt;</description></item>`)))
	}))
	defer server.Close()

	f := youtube.NewFetcher(server.URL, []youtube.AccountConfig{{Name: "Springfield Isotopes", ChannelID: "UCxxxx", SourceType: "hard_news", MaxAge: 365 * 24 * time.Hour}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("articles = %+v", articles)
	}
	if want := "Watch now"; articles[0].Summary != want {
		t.Errorf("Summary = %q, want %q (href/target/blank/nofollow must not leak)", articles[0].Summary, want)
	}
}

func TestFetch_ParsesChannelUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channel/UCxxxx" {
			t.Errorf("path = %q, want /channel/UCxxxx", r.URL.Path)
		}
		_, _ = w.Write([]byte(rssFeed(`<item><title>Isotopes highlights</title><link>https://www.youtube.com/watch?v=abc</link><guid>abc</guid></item>`)))
	}))
	defer server.Close()

	f := youtube.NewFetcher(server.URL, []youtube.AccountConfig{{Name: "Springfield Isotopes", ChannelID: "UCxxxx", SourceType: "hard_news", MaxAge: 365 * 24 * time.Hour}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 || articles[0].Title != "Isotopes highlights" {
		t.Fatalf("articles = %+v", articles)
	}
	if articles[0].Source.Type != "youtube" || articles[0].Source.Name != "Springfield Isotopes" {
		t.Errorf("Source = %+v", articles[0].Source)
	}
	if want := mustID(t, source.TypeYouTube, "UCxxxx"); articles[0].Source.ID != want {
		t.Errorf("Source.ID = %q, want %q (case-preserved)", articles[0].Source.ID, want)
	}
}

func TestFetch_DropsStaleItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(`<item><title>Old video</title><link>https://www.youtube.com/watch?v=old</link><guid>old</guid><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate></item>`)))
	}))
	defer server.Close()

	f := youtube.NewFetcher(server.URL, []youtube.AccountConfig{{ChannelID: "UCxxxx", MaxAge: 72 * time.Hour}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none (stale)", articles)
	}
}

func TestFetch_DropsBridgeErrorItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(
			`<item><title>Bridge returned error 500! (20701)</title><guid>err</guid><description>RSS-Bridge tried to fetch a website.</description></item>`,
		)))
	}))
	defer server.Close()

	f := youtube.NewFetcher(server.URL, []youtube.AccountConfig{{ChannelID: "UCxxxx", MaxAge: 365 * 24 * time.Hour}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 0 {
		t.Fatalf("articles = %+v, want no articles from an RSS-Bridge diagnostic feed", articles)
	}
}

func TestFetch_DedupsWithinCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFeed(
			`<item><title>A</title><link>https://www.youtube.com/watch?v=1</link><guid>1</guid></item>` +
				`<item><title>A again</title><link>https://www.youtube.com/watch?v=1</link><guid>1</guid></item>`,
		)))
	}))
	defer server.Close()

	f := youtube.NewFetcher(server.URL, []youtube.AccountConfig{{ChannelID: "UCxxxx"}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Errorf("articles = %+v, want 1 (deduped)", articles)
	}
}

func TestFetch_OneChannelFailureDoesNotStopOthers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	f := youtube.NewFetcher(server.URL, []youtube.AccountConfig{{ChannelID: "UCbad1"}, {ChannelID: "UCbad2"}}, testClientConfig(), noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v, want nil (one channel's failure must not abort the run)", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none (both point at the failing server)", articles)
	}
}

func TestFetch_ContextCancellationPropagates(t *testing.T) {
	f := youtube.NewFetcher("http://127.0.0.1:0", nil, testClientConfig(), noopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx); err == nil {
		t.Fatal("Fetch() error = nil, want context.Canceled")
	}
}
