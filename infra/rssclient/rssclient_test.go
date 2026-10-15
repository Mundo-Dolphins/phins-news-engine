package rssclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
)

const sampleRSS = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Sample</title>
<item><title>Item One</title><link>https://example.com/1</link><guid>1</guid></item>
</channel></rss>`

func testConfig() rssclient.Config {
	return rssclient.Config{
		Timeout:            2 * time.Second,
		MaxRetries:         2,
		BackoffBase:        5 * time.Millisecond,
		BackoffMax:         50 * time.Millisecond,
		ResponseLimitBytes: 1 << 20,
		// httptest servers listen on loopback, which the default policy blocks.
		AllowPrivateHosts: true,
	}
}

func TestFetch_ParsesValidFeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleRSS))
	}))
	defer server.Close()

	feed, err := rssclient.Fetch(context.Background(), testConfig(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(feed.Items) != 1 || feed.Items[0].Title != "Item One" {
		t.Errorf("feed.Items = %+v", feed.Items)
	}
}

func TestFetch_RejectsRSSBridgeErrorAtomFeed(t *testing.T) {
	bridgeErrorAtom := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title type="text">Username sprisotopes - Picnob</title>
  <generator>instagram-rss/RSS-Bridge</generator>
  <entry>
    <title type="html">Bridge returned error 403! (20704)</title>
    <published>2026-09-08T07:47:50+00:00</published>
    <updated>2026-09-08T07:47:50+00:00</updated>
    <id>urn:sha1:080ada07993e386c31852e7ee8892679a9af0c80</id>
    <link rel="alternate" type="text/html" href="http://host.containers.internal:3000/?action=display&amp;bridge=Picnob&amp;context=Username&amp;u=sprisotopes&amp;format=Atom"/>
    <content type="html">&lt;div class="error"&gt;&lt;h2&gt;The website is protected by CloudFlare&lt;/h2&gt;&lt;p&gt;RSS-Bridge tried to fetch a website.&lt;/p&gt;&lt;h2&gt;403 Forbidden&lt;/h2&gt;&lt;/div&gt;</content>
  </entry>
</feed>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(bridgeErrorAtom))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.MaxRetries = 0
	_, err := rssclient.Fetch(context.Background(), cfg, server.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want RSS-Bridge diagnostic feed rejected")
	}
	if !strings.Contains(err.Error(), "Bridge returned error 403") {
		t.Fatalf("Fetch() error = %q, want bridge status in error", err.Error())
	}
}

func TestFetch_SendsBrowserUserAgent(t *testing.T) {
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.UserAgent()
		_, _ = w.Write([]byte(sampleRSS))
	}))
	defer server.Close()

	if _, err := rssclient.Fetch(context.Background(), testConfig(), server.URL); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !strings.HasPrefix(gotUserAgent, "Mozilla/5.0 ") || !strings.Contains(gotUserAgent, "Chrome/") {
		t.Fatalf("User-Agent = %q, want a Chrome desktop browser agent", gotUserAgent)
	}
}

func TestFetch_RetriesOn5xxThenSucceeds(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(sampleRSS))
	}))
	defer server.Close()

	feed, err := rssclient.Fetch(context.Background(), testConfig(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(feed.Items) != 1 {
		t.Errorf("feed.Items = %+v", feed.Items)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestFetch_ExhaustsRetriesReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.MaxRetries = 1
	_, err := rssclient.Fetch(context.Background(), cfg, server.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want error after retries exhausted")
	}
}

func TestFetch_RetryAfterOn429IsHonored(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(sampleRSS))
	}))
	defer server.Close()

	var delays []time.Duration
	defer rssclient.SetSleepForTest(func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	})()

	cfg := testConfig()
	cfg.MaxRetries = 1
	if _, err := rssclient.Fetch(context.Background(), cfg, server.URL); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Errorf("requested delays = %v, want [1s] (Retry-After honored)", delays)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

func TestFetch_ResponseExceedingLimitErrors(t *testing.T) {
	big := strings.Repeat("a", 200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleRSS + "<!--" + big + "-->"))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.ResponseLimitBytes = 50
	cfg.MaxRetries = 0
	_, err := rssclient.Fetch(context.Background(), cfg, server.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want error for oversized response")
	}
}

func TestFetch_ContextCancellationStopsRetrying(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := testConfig()
	cfg.MaxRetries = 3
	_, err := rssclient.Fetch(ctx, cfg, server.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want error for a cancelled context")
	}
}
