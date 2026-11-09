package entities

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

const rosterCSV = "full_name,team,position,status,gsis_id,week\nZeno Ashbury,WAV,LB,ACT,g1,5\n"

func TestRosterSync_PathFlagBypassesTheConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(rosterCSV)) }))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "roster.csv")

	shared.ConfigPath = filepath.Join(t.TempDir(), "does-not-exist.yml") // must not be read
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newRosterSyncCommand()
	cmd.SetArgs([]string{"--path", dest, "--url", srv.URL})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync with --path failed: %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != rosterCSV || !strings.Contains(out.String(), "1 players") {
		t.Fatalf("dest = %q, output = %q", b, out.String())
	}
}

func TestRosterSync_BadDownloadKeepsTheExistingRoster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>rate limited</html>")) }))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(dest, []byte("good roster"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newRosterSyncCommand()
	cmd.SetArgs([]string{"--path", dest, "--url", srv.URL})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("a non-roster body must be an error")
	}
	if b, _ := os.ReadFile(dest); string(b) != "good roster" {
		t.Fatalf("dest = %q; the previous roster must survive a bad download", b)
	}
}

func TestSyncRoster_WritesTheConfiguredPathAndKeepsTheOldFileOnABadDownload(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(rosterCSV)) }))
	defer good.Close()
	dest := filepath.Join(t.TempDir(), "roster.csv")
	n, got, err := SyncRoster(context.Background(), config.RosterConfig{Enabled: true, Path: dest, URL: good.URL}, "", "")
	if err != nil || n != 1 || got != dest {
		t.Fatalf("SyncRoster() = %d, %q, %v", n, got, err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>rate limited</html>")) }))
	defer bad.Close()
	if _, _, err := SyncRoster(context.Background(), config.RosterConfig{Enabled: true, Path: dest, URL: bad.URL}, "", ""); err == nil {
		t.Fatal("a non-roster body must be an error")
	}
	if b, _ := os.ReadFile(dest); string(b) != rosterCSV {
		t.Fatalf("dest = %q; the previous roster must survive", b)
	}
	if _, _, err := SyncRoster(context.Background(), config.RosterConfig{}, "", ""); err == nil {
		t.Fatal("no destination at all must be an error")
	}
}
