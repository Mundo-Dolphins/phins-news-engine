package rostersync_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse/rostersync"
)

func TestDownload_WritesAtomicallyAndRejectsHTTPErrors(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("full_name,team\n"))
	}))
	defer ok.Close()
	dest := filepath.Join(t.TempDir(), "roster.csv")
	if err := rostersync.Download(context.Background(), ok.Client(), ok.URL, dest, nil); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "full_name,team\n" {
		t.Fatalf("dest content = %q", b)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer bad.Close()
	dest2 := filepath.Join(t.TempDir(), "roster.csv")
	if err := rostersync.Download(context.Background(), bad.Client(), bad.URL, dest2, nil); err == nil {
		t.Fatal("Download() error = nil, want error on HTTP 404")
	}
	if _, err := os.Stat(dest2); !os.IsNotExist(err) {
		t.Fatal("a failed download must not leave a destination file behind")
	}
}

func TestDownload_RejectedByValidatorKeepsExistingFile(t *testing.T) {
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>rate limited</html>"))
	}))
	defer garbage.Close()
	dest := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(dest, []byte("good roster"), 0o600); err != nil {
		t.Fatal(err)
	}
	validate := func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = nflverse.ParseRoster(f)
		return err
	}
	if err := rostersync.Download(context.Background(), garbage.Client(), garbage.URL, dest, validate); err == nil {
		t.Fatal("Download() error = nil, want the validator's error for a non-roster body")
	}
	if b, _ := os.ReadFile(dest); string(b) != "good roster" {
		t.Fatalf("dest = %q; a rejected download must leave the previous roster untouched", b)
	}
}
