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
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

const rosterCSV = "full_name,team,position,status,gsis_id,week\nZeno Ashbury,WAV,LB,ACT,g1,5\n"

func writeRoster(t *testing.T, mtime time.Time, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadLeaguePlayers(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		cfg  func() config.RosterConfig
		want bool
	}{
		"fresh file loads": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: writeRoster(t, now.Add(-24*time.Hour), rosterCSV), MaxAgeDays: 7}
		}, true},
		"stale file disables the stage": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: writeRoster(t, now.Add(-10*24*time.Hour), rosterCSV), MaxAgeDays: 7}
		}, false},
		"corrupt file disables the stage": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: writeRoster(t, now, "not,a,roster\n"), MaxAgeDays: 7}
		}, false},
		"missing file disables the stage": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "nope.csv"), MaxAgeDays: 7}
		}, false},
		"disabled block": {func() config.RosterConfig { return config.RosterConfig{} }, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := len(loadLeaguePlayers(tc.cfg(), now, nopLogger{})) > 0
			if got != tc.want {
				t.Fatalf("loadLeaguePlayers() non-empty is %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseEngineGroups(t *testing.T) {
	got := parseEngineGroups([]string{"bing, yahoo", " ", ",", "yep"})
	if len(got) != 2 || len(got[0]) != 2 || got[0][1] != "yahoo" || got[1][0] != "yep" {
		t.Fatalf("parseEngineGroups() = %v, want [[bing yahoo] [yep]]", got)
	}
	if parseEngineGroups(nil) != nil {
		t.Fatal("no groups must stay nil so SearXNG keeps all its engines")
	}
}

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
