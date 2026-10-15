package llmclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

var discoverInput = []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}}

func noWait(t *testing.T) {
	t.Helper()
	t.Cleanup(llmclient.SetSleepForTest(func(context.Context, time.Duration) error { return nil }))
}

func TestClient_TimesOutOnHangingServer(t *testing.T) {
	c := llmclient.New(llmclient.Config{BaseURL: httpfault.Hanging(t).URL, Model: "m", Timeout: 100 * time.Millisecond})
	start := time.Now()
	if _, err := c.Discover(context.Background(), discoverInput); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("call took %v", time.Since(start))
	}
}

func TestClient_ZeroTimeoutGetsDefault(t *testing.T) {
	if got := llmclient.TransportTimeoutForTest(llmclient.Config{}); got <= 0 {
		t.Fatalf("timeout = %v, want a positive default", got)
	}
}

func TestClient_RejectsOversizedBody(t *testing.T) {
	noWait(t)
	c := llmclient.New(llmclient.Config{BaseURL: httpfault.Oversized(t, int(netguard.JSONBodyLimit)+1).URL, Model: "m", Timeout: 5 * time.Second})
	_, err := c.Discover(context.Background(), discoverInput)
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestClient_ErrorsNeverContainToken(t *testing.T) {
	noWait(t)
	for _, base := range []string{
		httpfault.Closing(t).URL + "/v1?key=" + httpfault.Secret,
		"http://example.com/%zz?key=" + httpfault.Secret,
		"ftp://llm.example.com/?key=" + httpfault.Secret,
	} {
		c := llmclient.New(llmclient.Config{BaseURL: base, APIKey: httpfault.Secret, Model: "m", Timeout: 2 * time.Second})
		_, err := c.Discover(context.Background(), discoverInput)
		if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
			t.Fatalf("base %q: err = %v, must be non-nil and secret-free", base, err)
		}
	}
}

func TestClient_StopsRedirectLoop(t *testing.T) {
	noWait(t)
	srv, hits := httpfault.RedirectLoop(t)
	c := llmclient.New(llmclient.Config{BaseURL: srv.URL, Model: "m", Timeout: 2 * time.Second})
	if _, err := c.Discover(context.Background(), discoverInput); err == nil {
		t.Fatal("want error")
	}
	if got := hits.Load(); got != netguard.MaxRedirects+1 {
		t.Fatalf("hits = %d, want %d", got, netguard.MaxRedirects+1)
	}
}

func TestClient_RefusesLinkLocalBaseURL(t *testing.T) {
	c := llmclient.New(llmclient.Config{BaseURL: "http://169.254.169.254/v1", Model: "m", Timeout: time.Second})
	_, err := c.Discover(context.Background(), discoverInput)
	if !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Fatalf("err = %v, want ErrDisallowedURL", err)
	}
}

func TestClient_SendsNeutralUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		_, _ = w.Write(chatCompletionResponse(t, `{"entities":[]}`))
	}))
	defer srv.Close()
	c := llmclient.New(llmclient.Config{BaseURL: srv.URL, Model: "m", Timeout: 2 * time.Second})
	if _, err := c.Discover(context.Background(), discoverInput); err != nil {
		t.Fatal(err)
	}
	if ua != netguard.DefaultUserAgent {
		t.Fatalf("User-Agent = %q", ua)
	}
}
