// adapters/outbound/bluesky/client_test.go
package bluesky_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/bluesky"
)

const samplePostJSON = `{
  "feed": [
    {
      "post": {
        "uri": "at://did:plc:abc/app.bsky.feed.post/xyz",
        "author": {"handle": "isotopes.bsky.social", "displayName": "Springfield Isotopes"},
        "record": {
          "text": "Isotopes sign a new long snapper today",
          "createdAt": "2026-07-20T10:00:00.000Z",
          "facets": [{"features": [{"$type": "app.bsky.richtext.facet#link", "uri": "https://example.com/story"}]}]
        }
      }
    }
  ]
}`

func TestGetAuthorFeed_ParsesResponseAndSetsQueryParams(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(samplePostJSON))
	}))
	defer server.Close()

	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = server.URL
	posts, err := c.GetAuthorFeed(context.Background(), "isotopes.bsky.social", 50)
	if err != nil {
		t.Fatalf("GetAuthorFeed() error = %v", err)
	}
	if gotPath != "/xrpc/app.bsky.feed.getAuthorFeed" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "actor=isotopes.bsky.social") || !strings.Contains(gotQuery, "limit=50") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(posts) != 1 || posts[0].Post.Record.Text != "Isotopes sign a new long snapper today" {
		t.Fatalf("posts = %+v", posts)
	}
}

func TestGetFeed_SetsFeedQueryParam(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"feed": []}`))
	}))
	defer server.Close()

	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = server.URL
	if _, err := c.GetFeed(context.Background(), "at://did:plc:x/app.bsky.feed.generator/y", 50); err != nil {
		t.Fatalf("GetFeed() error = %v", err)
	}
	if !strings.Contains(gotQuery, "feed=at%3A%2F%2Fdid%3Aplc%3Ax") {
		t.Errorf("query = %q, want URL-encoded feed param", gotQuery)
	}
}

func TestGetAuthorFeed_NonOKStatusErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = server.URL
	if _, err := c.GetAuthorFeed(context.Background(), "x", 50); err == nil {
		t.Fatal("GetAuthorFeed() error = nil, want error for HTTP 500")
	}
}

func TestGetAuthorFeed_NonOKStatusIncludesAPIMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"InvalidRequest","message":"Profile not found"}`))
	}))
	defer server.Close()

	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = server.URL
	_, err := c.GetAuthorFeed(context.Background(), "missing.bsky.social", 50)
	if err == nil {
		t.Fatal("GetAuthorFeed() error = nil, want error for HTTP 400")
	}
	if !strings.Contains(err.Error(), "Profile not found") || !strings.Contains(err.Error(), "InvalidRequest") {
		t.Fatalf("GetAuthorFeed() error = %q, want Bluesky API error details", err.Error())
	}
}

func TestGetAuthorFeed_ResponseExceedingLimitErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"feed": [], "padding": "` + strings.Repeat("a", 200) + `"}`))
	}))
	defer server.Close()

	c := bluesky.NewClient(2*time.Second, 50)
	c.Host = server.URL
	if _, err := c.GetAuthorFeed(context.Background(), "x", 50); err == nil {
		t.Fatal("GetAuthorFeed() error = nil, want error for oversized response")
	}
}

func TestGetAuthorFeed_InvalidJSONErrorsWithoutPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = server.URL
	if _, err := c.GetAuthorFeed(context.Background(), "x", 50); err == nil {
		t.Fatal("GetAuthorFeed() error = nil, want error for invalid JSON")
	}
}
