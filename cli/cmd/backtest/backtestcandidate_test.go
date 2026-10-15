package backtest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

func TestBacktestCandidate_MissingRequiredFlags(t *testing.T) {
	_, err := testsupport.Execute(t, "backtest", "candidate")
	if err == nil {
		t.Fatal("testsupport.Execute() error = nil, want a usage error for missing --db")
	}
}

func TestBacktestCandidate_MissingCandidatePath(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")

	_, err := testsupport.Execute(t, "backtest", "candidate", "--config", cfgPath, "--db", dbPath)
	if err == nil {
		t.Fatal("testsupport.Execute() error = nil, want a usage error for missing --candidate-dictionary/--candidate-model")
	}
}

func TestBacktestCandidate_EndToEndAgainstFixtureData(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t) // classification.enabled: false in the base fixture — this test only
	// exercises flag wiring and the summary/CSV output shape, not classification behavior
	// (backtestcandidate's own package tests already cover the outcome-quadrant classification logic).

	// The base fixture points source_policy.path at the repo-relative
	// "configs/source-policies.yml", which doesn't resolve from this
	// package's test working directory. backtestcandidate.Run actually
	// loads source policies, so point it at a minimal, real fixture file
	// instead (the same fix other backtest command tests apply).
	sourcePoliciesPath := filepath.Join(filepath.Dir(cfgPath), "source-policies.yml")
	if err := os.WriteFile(sourcePoliciesPath, []byte("version: 1\npolicies: []\n"), 0o600); err != nil {
		t.Fatalf("write source-policies fixture: %v", err)
	}
	cfgContents, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read fixture config: %v", err)
	}
	cfgContents = []byte(strings.Replace(string(cfgContents), "  path: configs/source-policies.yml", "  path: "+sourcePoliciesPath, 1))
	if err := os.WriteFile(cfgPath, cfgContents, 0o600); err != nil {
		t.Fatalf("rewrite fixture config: %v", err)
	}

	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	dir := t.TempDir()
	candidateDictPath := filepath.Join(dir, "candidate-dictionary.yml")
	if err := os.WriteFile(candidateDictPath, []byte(minimalCandidateDictionaryYAML), 0o600); err != nil {
		t.Fatalf("write candidate dictionary fixture: %v", err)
	}
	outPath := filepath.Join(dir, "report.csv")

	out, err := testsupport.Execute(t, "backtest", "candidate",
		"--days", "30", "--db", dbPath, "--candidate-dictionary", candidateDictPath,
		"--config", cfgPath, "--out", outPath)
	if err != nil {
		t.Fatalf("testsupport.Execute() error = %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "backtest candidate: analyzed") {
		t.Errorf("output missing summary line:\n%s", out)
	}
	if !strings.Contains(out, "backtest candidate progress: loading") {
		t.Errorf("output missing progress line:\n%s", out)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("expected CSV report at %s: %v", outPath, err)
	}
}

const minimalCandidateDictionaryYAML = `
meta:
  version: "candidate-1.0.0"
  name: candidate_dictionary
  language: en

decision:
  positive_threshold_high: 10
  positive_threshold_medium: 6
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3

categories:
  official_names:
    weight: 12
    strong_override: true
    entries:
      - canonical_name: "Springfield Isotopes"
        type: official_name
        source: manual
        confidence: 1.0
`
