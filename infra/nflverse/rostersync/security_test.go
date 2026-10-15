package rostersync_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse/rostersync"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

func TestDownload_TimesOutOnHangingServer(t *testing.T) {
	srv := httpfault.Hanging(t)
	start := time.Now()
	err := rostersync.Download(context.Background(), &http.Client{Timeout: 100 * time.Millisecond}, srv.URL, filepath.Join(t.TempDir(), "r.csv"), nil)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("call took %v", time.Since(start))
	}
}

func TestDownload_ClientWithoutTimeoutGetsDefault(t *testing.T) {
	if got := rostersync.GuardedClientForTest(&http.Client{}).Timeout; got <= 0 {
		t.Fatalf("timeout = %v, want a positive default", got)
	}
	if got := rostersync.GuardedClientForTest(nil); got.Timeout <= 0 || got.CheckRedirect == nil {
		t.Fatalf("nil client not replaced by a guarded default: %+v", got)
	}
}

func TestDownload_ErrorsNeverContainToken(t *testing.T) {
	for _, u := range []string{
		httpfault.Closing(t).URL + "/roster.csv?token=" + httpfault.Secret,
		"http://example.com/%zz?token=" + httpfault.Secret,
		"ftp://files.example.com/roster.csv?token=" + httpfault.Secret,
	} {
		err := rostersync.Download(context.Background(), &http.Client{Timeout: 2 * time.Second}, u, filepath.Join(t.TempDir(), "r.csv"), nil)
		if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
			t.Fatalf("url %q: err = %v, must be non-nil and secret-free", u, err)
		}
	}
}

func TestDownload_StopsRedirectLoop(t *testing.T) {
	srv, hits := httpfault.RedirectLoop(t)
	err := rostersync.Download(context.Background(), &http.Client{Timeout: 2 * time.Second}, srv.URL, filepath.Join(t.TempDir(), "r.csv"), nil)
	if err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != netguard.MaxRedirects+1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestDownload_RefusesLinkLocalURL(t *testing.T) {
	err := rostersync.Download(context.Background(), nil, "http://169.254.169.254/roster.csv", filepath.Join(t.TempDir(), "r.csv"), nil)
	if !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Fatalf("err = %v, want ErrDisallowedURL", err)
	}
}

func TestDownload_WritesPrivateFileAndDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("full_name,team\n"))
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "nested")
	dest := filepath.Join(dir, "roster.csv")
	if err := rostersync.Download(context.Background(), srv.Client(), srv.URL, dest, nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir mode = %o, want 700", perm)
	}
}
