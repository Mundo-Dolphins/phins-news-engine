package editorialveto_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	infraeditorialveto "github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
)

func writeVetoFile(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vetoes.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoad_ParsesRulesInFileOrder(t *testing.T) {
	path := writeVetoFile(t, `
version: "veto-test-1"
rules:
  - category: gambling
    reason_code: "veto:gambling:sportsbook_promo"
    patterns: ["free bets", "odds boost"]
  - category: spam
    reason_code: "veto:spam:generic"
    patterns: ["click here now"]
`)
	cfg, err := infraeditorialveto.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Version != "veto-test-1" {
		t.Errorf("Version = %q, want %q", cfg.Version, "veto-test-1")
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("len(Rules) = %d, want 2", len(cfg.Rules))
	}
	if cfg.Rules[0].Category != editorialveto.CategoryGambling || cfg.Rules[0].ReasonCode != "veto:gambling:sportsbook_promo" {
		t.Errorf("Rules[0] = %+v, want gambling/veto:gambling:sportsbook_promo", cfg.Rules[0])
	}
	if cfg.Rules[1].Category != editorialveto.CategorySpam {
		t.Errorf("Rules[1].Category = %q, want %q", cfg.Rules[1].Category, editorialveto.CategorySpam)
	}
}

func TestLoad_UnknownFieldFailsClosed(t *testing.T) {
	path := writeVetoFile(t, `
version: "veto-test-1"
rules:
  - category: gambling
    reason_code: "x"
    patterns: ["y"]
    typo_field: "oops"
`)
	if _, err := infraeditorialveto.Load(path); err == nil {
		t.Fatal("Load() error = nil, want an error for an unknown field")
	}
}

func TestLoad_InvalidRegexPatternFailsClosed(t *testing.T) {
	path := writeVetoFile(t, `
version: "veto-test-1"
rules:
  - category: noise
    reason_code: "x"
    patterns: ["["]
`)
	if _, err := infraeditorialveto.Load(path); err == nil {
		t.Fatal("Load() error = nil, want an error for an invalid regex pattern")
	}
}

func TestLoad_MissingFileReturnsError(t *testing.T) {
	if _, err := infraeditorialveto.Load(filepath.Join(t.TempDir(), "does-not-exist.yml")); err == nil {
		t.Fatal("Load() error = nil, want an error for a missing file")
	}
}
