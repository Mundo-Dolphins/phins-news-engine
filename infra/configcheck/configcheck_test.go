package configcheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/configcheck"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// validConfig writes a settings file whose relative catalog paths point at
// the repository examples.
func validConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	path := testsupport.WriteFixture(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.ReplaceAll(string(body), "configs/sources.yml", repofs.ExampleConfig(t, "sources.yml"))
	out = strings.ReplaceAll(out, "configs/source-policies.yml", repofs.ExampleConfig(t, "source-policies.yml"))
	out += "\nfetch_sources:\n  rss: {enabled: true}\n"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateReportsCapabilities(t *testing.T) {
	path := validConfig(t)
	rep, err := configcheck.Validate(path, config.ProfileLocal, fetcherwiring.Extensions{})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if rep.Profile != config.ProfileLocal || rep.Config == nil || len(rep.Active) == 0 {
		t.Errorf("report = %+v", rep)
	}
}

func TestValidateMissingFile(t *testing.T) {
	if _, err := configcheck.Validate(filepath.Join(t.TempDir(), "nope.yml"), config.ProfileLocal, fetcherwiring.Extensions{}); err == nil {
		t.Error("Validate() error = nil, want an error for a missing file")
	}
}

func TestValidateBrokenSourcesCatalog(t *testing.T) {
	path := validConfig(t)
	body, _ := os.ReadFile(path)
	broken := filepath.Join(t.TempDir(), "sources.yml")
	if err := os.WriteFile(broken, []byte("rss_feeds: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := strings.ReplaceAll(string(body), repofs.ExampleConfig(t, "sources.yml"), broken)
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := configcheck.Validate(path, config.ProfileLocal, fetcherwiring.Extensions{})
	if err == nil || !strings.Contains(err.Error(), "load sources") {
		t.Errorf("Validate() error = %v, want a load sources error", err)
	}
}
