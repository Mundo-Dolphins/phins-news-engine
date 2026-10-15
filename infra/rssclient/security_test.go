package rssclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
)

const secretToken = "SECRET-TOKEN-123"

// hijackAndClose drops the connection without writing a response.
func hijackAndClose(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer cannot hijack")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestFetch_TimesOutOnHangingServer(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	cfg := testConfig()
	cfg.Timeout = 100 * time.Millisecond
	cfg.MaxRetries = 0
	start := time.Now()
	_, err := rssclient.Fetch(context.Background(), cfg, server.URL)
	if err == nil {
		t.Fatal("Fetch() against a hanging server returned nil error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Fetch() took %v, want about the 100ms timeout", elapsed)
	}
}

func TestFetch_ZeroTimeoutGetsDefault(t *testing.T) {
	if got := rssclient.HTTPClientForTest(rssclient.Config{}).Timeout; got <= 0 {
		t.Fatalf("client timeout = %v, want a positive default", got)
	}
}

func TestFetch_RejectsOversizedBodyExplicitly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.ResponseLimitBytes = 1024
	cfg.MaxRetries = 0
	_, err := rssclient.Fetch(context.Background(), cfg, server.URL)
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestFetch_ErrorsNeverContainToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijackAndClose(t, w)
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.MaxRetries = 0
	for _, feedURL := range []string{
		server.URL + "/feed?token=" + secretToken,
		server.URL + "/%zz?token=" + secretToken,
		"ftp://feeds.example.com/?token=" + secretToken,
	} {
		_, err := rssclient.Fetch(context.Background(), cfg, feedURL)
		if err == nil || strings.Contains(err.Error(), secretToken) {
			t.Fatalf("err = %v, must be non-nil and secret-free", err)
		}
	}
}

func TestFetch_StopsRedirectLoop(t *testing.T) {
	var hits atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, server.URL+"/again", http.StatusFound)
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.MaxRetries = 0
	if _, err := rssclient.Fetch(context.Background(), cfg, server.URL); err == nil {
		t.Fatal("redirect loop returned nil error")
	}
	if got := hits.Load(); got != netguard.MaxRedirects+1 {
		t.Fatalf("hits = %d, want %d", got, netguard.MaxRedirects+1)
	}
}

func TestFetch_BlocksMetadataAndPrivateHostsByDefault(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(sampleRSS))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.AllowPrivateHosts = false
	for _, feedURL := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:1/feed",
		"http://[::1]:1/feed",
		"http://10.0.0.1/feed",
		"http://192.168.0.10/feed",
		strings.Replace(server.URL, "127.0.0.1", "localhost", 1),
		"file:///etc/passwd",
		"http://user:pw@feeds.example.com/feed",
	} {
		_, err := rssclient.Fetch(context.Background(), cfg, feedURL)
		if !errors.Is(err, netguard.ErrDisallowedURL) {
			t.Errorf("Fetch(%q) err = %v, want ErrDisallowedURL", feedURL, err)
		}
	}
	// The loopback server itself is also refused at dial time.
	if _, err := rssclient.Fetch(context.Background(), cfg, server.URL); !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Errorf("Fetch(loopback server) err = %v, want ErrDisallowedURL", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("blocked server was reached %d times", hits.Load())
	}
}

func TestFetch_DisallowedURLIsNotRetried(t *testing.T) {
	var sleeps atomic.Int32
	restore := rssclient.SetSleepForTest(func(context.Context, time.Duration) error {
		sleeps.Add(1)
		return nil
	})
	defer restore()
	cfg := testConfig()
	cfg.AllowPrivateHosts = false
	if _, err := rssclient.Fetch(context.Background(), cfg, "http://169.254.169.254/"); err == nil {
		t.Fatal("want error")
	}
	if sleeps.Load() != 0 {
		t.Fatalf("disallowed URL retried %d times", sleeps.Load())
	}
}

func TestParseBytes_OversizeMatchesErrBodyTooLarge(t *testing.T) {
	_, err := rssclient.ParseBytes([]byte(strings.Repeat("x", 11)), 10)
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	if !strings.Contains(err.Error(), "exceeds 10 bytes") {
		t.Errorf("err = %q, want the limit in the message", err)
	}
}

// An operator-run server (allowed under AllowPrivateHosts) that redirects
// to the link-local metadata address must not get the fetcher there.
func TestFetch_AllowPrivateRefusesRedirectToMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.AllowPrivateHosts = true
	if _, err := rssclient.Fetch(context.Background(), cfg, server.URL); !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Fatalf("err = %v, want ErrDisallowedURL", err)
	}
}
