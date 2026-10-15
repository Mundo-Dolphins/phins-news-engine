package bluesky_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/bluesky"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
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

func authorFeedJSON(posts string) string {
	return `{"feed": [` + posts + `]}`
}

func TestFetch_ParsesAccountPostWithExternalLink(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(authorFeedJSON(sprintfPost(now))))
	}))
	defer server.Close()

	client := bluesky.NewClient(2*time.Second, 1<<20)
	client.Host = server.URL
	f := bluesky.NewFetcher([]bluesky.AccountConfig{{Handle: "isotopes.bsky.social", Name: "Springfield Isotopes", SourceType: "hard_news", MaxAge: 24 * time.Hour}}, nil, client, noopLogger{})

	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("articles = %+v, want 1", articles)
	}
	a := articles[0]
	if a.OriginalURL != "https://example.com/story" {
		t.Errorf("OriginalURL = %q, want the embedded external link", a.OriginalURL)
	}
	if a.ExternalID != "at://did:plc:abc/app.bsky.feed.post/1" {
		t.Errorf("ExternalID = %q", a.ExternalID)
	}
	if a.Author != "Springfield Isotopes" {
		t.Errorf("Author = %q", a.Author)
	}
	if a.Source.Type != "bluesky" {
		t.Errorf("Source.Type = %q", a.Source.Type)
	}
	if want := mustID(t, source.TypeBluesky, "isotopes.bsky.social"); a.Source.ID != want {
		t.Errorf("Source.ID = %q, want %q", a.Source.ID, want)
	}
}

func sprintfPost(t time.Time) string {
	return `{
  "post": {
    "uri": "at://did:plc:abc/app.bsky.feed.post/1",
    "author": {"handle": "isotopes.bsky.social", "displayName": "Springfield Isotopes"},
    "record": {
      "text": "Big news for the team - full story below",
      "createdAt": "` + t.Format(time.RFC3339) + `",
      "facets": [{"features": [{"$type": "app.bsky.richtext.facet#link", "uri": "https://example.com/story"}]}]
    }
  }
}`
}

func TestFetch_DropsShortPosts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(authorFeedJSON(`{"post": {"uri": "at://x/1", "author": {"handle": "h"}, "record": {"text": "hi", "createdAt": "` + time.Now().UTC().Format(time.RFC3339) + `"}}}`)))
	}))
	defer server.Close()

	client := bluesky.NewClient(2*time.Second, 1<<20)
	client.Host = server.URL
	f := bluesky.NewFetcher([]bluesky.AccountConfig{{Handle: "h.bsky.social", MaxAge: 24 * time.Hour}}, nil, client, noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none (text too short)", articles)
	}
}

func TestFetch_DropsStalePosts(t *testing.T) {
	old := time.Now().UTC().Add(-72 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(authorFeedJSON(sprintfPost(old))))
	}))
	defer server.Close()

	client := bluesky.NewClient(2*time.Second, 1<<20)
	client.Host = server.URL
	f := bluesky.NewFetcher([]bluesky.AccountConfig{{Handle: "isotopes.bsky.social", MaxAge: 24 * time.Hour}}, nil, client, noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none (stale)", articles)
	}
}

func TestFetch_AppliesBlockedKeywordsAndSkipAuthors(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(authorFeedJSON(sprintfPost(now))))
	}))
	defer server.Close()

	client := bluesky.NewClient(2*time.Second, 1<<20)
	client.Host = server.URL

	blockedKeyword := bluesky.NewFetcher([]bluesky.AccountConfig{{Handle: "isotopes.bsky.social", MaxAge: 24 * time.Hour, BlockedKeywords: []string{"big news"}}}, nil, client, noopLogger{})
	if articles, err := blockedKeyword.Fetch(context.Background()); err != nil || len(articles) != 0 {
		t.Fatalf("blocked-keyword case: articles = %+v, err = %v, want none", articles, err)
	}

	skipAuthor := bluesky.NewFetcher([]bluesky.AccountConfig{{Handle: "isotopes.bsky.social", MaxAge: 24 * time.Hour, SkipAuthors: []string{"isotopes.bsky.social"}}}, nil, client, noopLogger{})
	if articles, err := skipAuthor.Fetch(context.Background()); err != nil || len(articles) != 0 {
		t.Fatalf("skip-author case: articles = %+v, err = %v, want none", articles, err)
	}
}

func TestFetch_CuratedFeedIsFetchedAlongsideAccounts(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("feed") != "" {
			_, _ = w.Write([]byte(authorFeedJSON(sprintfPost(now))))
			return
		}
		_, _ = w.Write([]byte(authorFeedJSON("")))
	}))
	defer server.Close()

	client := bluesky.NewClient(2*time.Second, 1<<20)
	client.Host = server.URL
	f := bluesky.NewFetcher(nil, []bluesky.FeedConfig{{URI: "at://did:plc:x/app.bsky.feed.generator/curated", Name: "Curated", MaxAge: 24 * time.Hour}}, client, noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("articles = %+v, want 1 from the curated feed", articles)
	}
	if want := mustID(t, source.TypeBluesky, "feed-"+source.Slug("Curated")); articles[0].Source.ID != want {
		t.Errorf("Source.ID = %q, want %q", articles[0].Source.ID, want)
	}
}

func TestFetch_OneAccountFailureDoesNotStopOthers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := bluesky.NewClient(2*time.Second, 1<<20)
	client.Host = server.URL
	f := bluesky.NewFetcher([]bluesky.AccountConfig{{Handle: "h1.bsky.social"}, {Handle: "h2.bsky.social"}}, nil, client, noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v, want nil (one account's failure must not abort the run)", err)
	}
	if len(articles) != 0 {
		t.Errorf("articles = %+v, want none (both point at the failing server)", articles)
	}
}

func TestFetch_ContextCancellationPropagates(t *testing.T) {
	client := bluesky.NewClient(2*time.Second, 1<<20)
	f := bluesky.NewFetcher(nil, nil, client, noopLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx); err == nil {
		t.Fatal("Fetch() error = nil, want context.Canceled")
	}
}
