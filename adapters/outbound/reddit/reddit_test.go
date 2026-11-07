// adapters/outbound/reddit/reddit_test.go
package reddit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/reddit"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
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

func atomFeed(entries string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>F</title>` + entries + `</feed>`
}

// redditEntry mirrors the Atom entry Reddit serves for a post: the post's
// target sits in the "[link]" anchor of the HTML content.
func redditEntry(id, link string) string {
	post := "https://www.reddit.com/r/sprisotopes/comments/" + id + "/x/"
	return `<entry><id>t3_` + id + `</id><title>Week 9 Friday report</title>` +
		`<link href="` + post + `"/>` +
		`<updated>2026-11-06T21:13:32+00:00</updated>` +
		`<content type="html">&lt;span&gt;&lt;a href=&quot;` + link + `&quot;&gt;[link]&lt;/a&gt;&lt;/span&gt; ` +
		`&lt;span&gt;&lt;a href=&quot;` + post + `&quot;&gt;[comments]&lt;/a&gt;&lt;/span&gt;</content></entry>`
}

func TestFetch_MediaImagesMetadata(t *testing.T) {
	tests := []struct {
		name  string
		link  string
		hosts []string
		want  string
	}{
		{name: "jpeg", link: "https://i.redd.it/uq0amutw520i1.jpeg", want: `[{"url":"https://i.redd.it/uq0amutw520i1.jpeg","type":"image/jpeg"}]`},
		{name: "png", link: "https://i.redd.it/a.png", want: `[{"url":"https://i.redd.it/a.png","type":"image/png"}]`},
		{name: "uppercase extension with query", link: "https://i.redd.it/ABC.JPEG?x=1", want: `[{"url":"https://i.redd.it/ABC.JPEG?x=1","type":"image/jpeg"}]`},
		{name: "text post", link: "https://www.reddit.com/r/sprisotopes/comments/p1/x/"},
		{name: "external host", link: "https://i.imgur.com/a.jpg"},
		{name: "gallery", link: "https://www.reddit.com/gallery/abc"},
		{name: "http", link: "http://i.redd.it/a.jpg"},
		{name: "webp", link: "https://i.redd.it/a.webp"},
		{name: "custom hosts", link: "https://img.example.test/a.png", hosts: []string{"img.example.test"}, want: `[{"url":"https://img.example.test/a.png","type":"image/png"}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(atomFeed(redditEntry("p1", tc.link))))
			}))
			defer server.Close()

			f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "sprisotopes"}}, testClientConfig(), noopLogger{})
			if tc.hosts != nil {
				f = f.WithImageHosts(tc.hosts...)
			}
			f.BaseURL = server.URL
			articles, err := f.Fetch(context.Background())
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if len(articles) != 1 {
				t.Fatalf("articles = %d, want 1", len(articles))
			}
			a := articles[0]
			got, ok := a.Metadata[article.MetadataMediaImages]
			if tc.want == "" {
				if ok || a.Metadata != nil {
					t.Fatalf("Metadata = %v, want nil", a.Metadata)
				}
				return
			}
			if got != tc.want {
				t.Errorf("media_images = %s, want %s", got, tc.want)
			}
			// Summary stays what it was before images were exposed: Reddit's
			// Atom feed carries no summary, only content.
			if a.Summary != "" {
				t.Errorf("Summary = %q, want empty", a.Summary)
			}
		})
	}
}
