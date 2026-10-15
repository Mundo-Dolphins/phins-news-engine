package espnscoreboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

func TestClient_TimesOutOnHangingServer(t *testing.T) {
	c := NewClient("spr", "", 100*time.Millisecond)
	c.Host = httpfault.Hanging(t).URL
	start := time.Now()
	if _, _, err := c.Current(context.Background(), time.Now()); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("call took %v", time.Since(start))
	}
}

func TestClient_ZeroTimeoutGetsDefault(t *testing.T) {
	if got := NewClient("spr", "", 0).httpClient.Timeout; got <= 0 {
		t.Fatalf("timeout = %v, want a positive default", got)
	}
}

func TestClient_RejectsOversizedBody(t *testing.T) {
	c := NewClient("spr", "", 5*time.Second)
	c.Host = httpfault.Oversized(t, int(maxResponseBytes)+1).URL
	_, _, err := c.Current(context.Background(), time.Now())
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestClient_ErrorsNeverContainToken(t *testing.T) {
	c := NewClient("spr", "", 2*time.Second)
	for _, host := range []string{
		httpfault.Closing(t).URL + "/?apikey=" + httpfault.Secret,
		"http://example.com/%zz?apikey=" + httpfault.Secret,
	} {
		c.Host = host
		_, _, err := c.Current(context.Background(), time.Now())
		if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
			t.Fatalf("err = %v, must be non-nil and secret-free", err)
		}
	}
}

func TestClient_StopsRedirectLoop(t *testing.T) {
	srv, hits := httpfault.RedirectLoop(t)
	c := NewClient("spr", "", 2*time.Second)
	c.Host = srv.URL
	if _, _, err := c.Current(context.Background(), time.Now()); err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != netguard.MaxRedirects+1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}
