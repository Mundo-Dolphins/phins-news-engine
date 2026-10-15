package wikidata

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

func securityConfig(base string) Config {
	return Config{BaseURL: base, Language: "en", UserAgent: testUA, Timeout: 2 * time.Second, MaxCandidates: 5, AllowPrivateHosts: true}
}

func TestClient_TimesOutOnHangingServer(t *testing.T) {
	cfg := securityConfig(httpfault.Hanging(t).URL)
	cfg.Timeout = 100 * time.Millisecond
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	start := time.Now()
	if _, err := c.Search(context.Background(), "q", 3); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("call took %v", time.Since(start))
	}
}

func TestClient_RejectsOversizedBody(t *testing.T) {
	cfg := securityConfig(httpfault.Oversized(t, 2048).URL)
	cfg.MaxResponseBytes = 1024
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(context.Background(), "q", 3); !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestClient_ErrorsNeverContainToken(t *testing.T) {
	c, err := New(securityConfig(httpfault.Closing(t).URL + "/w/api.php?token=" + httpfault.Secret))
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	_, err = c.Search(context.Background(), "q", 3)
	if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
		t.Fatalf("err = %v, must be non-nil and secret-free", err)
	}
	if !errors.Is(err, out.ErrTransient) {
		t.Fatalf("err = %v, a dropped connection must stay transient", err)
	}
	for _, base := range []string{
		"http://example.com/%zz?token=" + httpfault.Secret,
		"ftp://wikidata.example.org/?token=" + httpfault.Secret,
		"https://u:" + httpfault.Secret + "@wikidata.example.org/",
	} {
		_, err := New(securityConfig(base))
		if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
			t.Fatalf("New(%q) err = %v, must be non-nil and secret-free", base, err)
		}
	}
}

func TestClient_NeverFollowsRedirects(t *testing.T) {
	srv, hits := httpfault.RedirectLoop(t)
	c, err := New(securityConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(context.Background(), "q", 3); err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestNew_BlocksPrivateAndMetadataHostsByDefault(t *testing.T) {
	for _, base := range []string{
		"http://169.254.169.254/w/api.php",
		"http://127.0.0.1:8080/w/api.php",
		"http://[::1]/w/api.php",
		"http://10.0.0.5/w/api.php",
		"http://localhost/w/api.php",
	} {
		cfg := securityConfig(base)
		cfg.AllowPrivateHosts = false
		if _, err := New(cfg); !errors.Is(err, netguard.ErrDisallowedURL) {
			t.Errorf("New(%q) err = %v, want ErrDisallowedURL", base, err)
		}
	}
	cfg := securityConfig("http://169.254.169.254/w/api.php")
	if _, err := New(cfg); !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Errorf("link-local must stay blocked with AllowPrivateHosts, err = %v", err)
	}
}

func TestClient_DialTimePrivateHostIsNotTransient(t *testing.T) {
	// A hostname that passes New's literal check but resolves to a
	// forbidden address is refused by the dialer; that refusal is
	// configuration, not a transient outage.
	c, err := New(securityConfig(httpfault.Closing(t).URL))
	if err != nil {
		t.Fatal(err)
	}
	c.httpClient.Transport = netguard.NewTransport(netguard.BlockPrivate)
	_, err = c.Search(context.Background(), "q", 3)
	if !errors.Is(err, netguard.ErrDisallowedURL) || errors.Is(err, out.ErrTransient) {
		t.Fatalf("err = %v, want a non-transient ErrDisallowedURL", err)
	}
}
