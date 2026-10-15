package bluesky_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/bluesky"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

func TestClient_TimesOutOnHangingServer(t *testing.T) {
	srv := httpfault.Hanging(t)
	c := bluesky.NewClient(100*time.Millisecond, 1<<20)
	c.Host = srv.URL
	start := time.Now()
	if _, err := c.GetAuthorFeed(context.Background(), "a.example.com", 5); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("call took %v", time.Since(start))
	}
}

func TestClient_ZeroTimeoutGetsDefault(t *testing.T) {
	srv := httpfault.Hanging(t)
	c := bluesky.NewClient(0, 1<<20)
	c.Host = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.GetAuthorFeed(ctx, "a.example.com", 5); err == nil {
		t.Fatal("want error")
	}
	if got := bluesky.ClientTimeoutForTest(c); got <= 0 {
		t.Fatalf("timeout = %v, want a positive default", got)
	}
}

func TestClient_RejectsOversizedBody(t *testing.T) {
	srv := httpfault.Oversized(t, 4096)
	c := bluesky.NewClient(2*time.Second, 1024)
	c.Host = srv.URL
	_, err := c.GetAuthorFeed(context.Background(), "a.example.com", 5)
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestClient_ErrorsNeverContainToken(t *testing.T) {
	srv := httpfault.Closing(t)
	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = srv.URL
	_, err := c.GetAuthorFeed(context.Background(), httpfault.Secret, 5)
	if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
		t.Fatalf("err = %v, must be non-nil and secret-free", err)
	}
	c.Host = srv.URL + "/%zz?token=" + httpfault.Secret
	_, err = c.GetFeed(context.Background(), "at://example/feed", 5)
	if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
		t.Fatalf("err = %v, must be non-nil and secret-free", err)
	}
}

func TestClient_StopsRedirectLoop(t *testing.T) {
	srv, hits := httpfault.RedirectLoop(t)
	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = srv.URL
	if _, err := c.GetAuthorFeed(context.Background(), "a.example.com", 5); err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != netguard.MaxRedirects+1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestClient_SendsNeutralUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		_, _ = w.Write([]byte(`{"feed":[]}`))
	}))
	defer srv.Close()
	c := bluesky.NewClient(2*time.Second, 1<<20)
	c.Host = srv.URL
	if _, err := c.GetAuthorFeed(context.Background(), "a.example.com", 5); err != nil {
		t.Fatal(err)
	}
	if ua != netguard.DefaultUserAgent {
		t.Fatalf("User-Agent = %q", ua)
	}
}
