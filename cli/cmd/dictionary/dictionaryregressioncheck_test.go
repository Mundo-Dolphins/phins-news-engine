package dictionary_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

const regressionCheckFixtureYAML = `
meta:
  version: "1.0.0"
  name: isotopes
  language: en
  generated_at: "2026-08-01"
decision:
  positive_threshold_high: 1
  positive_threshold_medium: 0.5
  negative_threshold_high: -3
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  official_names:
    weight: 5
    strong_override: true
    entries:
      - canonical_name: "Springfield Isotopes"
        type: official_name
        confidence: 1
`

const regressionCheckFixtureNoEntityYAML = `
meta:
  version: "1.0.0"
  name: isotopes
  language: en
  generated_at: "2026-08-01"
decision:
  positive_threshold_high: 1
  positive_threshold_medium: 0.5
  negative_threshold_high: -3
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  official_names:
    weight: 5
    strong_override: true
    entries: []
`

func TestDictionaryRegressionCheck_NoFlipsAgainstItself(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.yml")
	if err := os.WriteFile(path, []byte(regressionCheckFixtureYAML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := testsupport.Execute(t, "dictionary", "regression-check", "--previous", path, "--candidate", path)
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "0/") {
		t.Errorf("expected 0 flips, got:\n%s", out)
	}
}

func TestDictionaryRegressionCheck_FlipsExceedingThresholdFailsCommand(t *testing.T) {
	dir := t.TempDir()
	prevPath := filepath.Join(dir, "prev.yml")
	candPath := filepath.Join(dir, "cand.yml")
	if err := os.WriteFile(prevPath, []byte(regressionCheckFixtureYAML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(candPath, []byte(regressionCheckFixtureNoEntityYAML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := testsupport.Execute(t, "dictionary", "regression-check", "--previous", prevPath, "--candidate", candPath)
	if err == nil {
		t.Fatalf("expected error when flips exceed the default threshold, output:\n%s", out)
	}
	if !strings.Contains(out, "flipped") {
		t.Errorf("expected output to report flip count, got:\n%s", out)
	}
}

func TestDictionaryRegressionCheck_CandidateRequired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.yml")
	if err := os.WriteFile(path, []byte(regressionCheckFixtureYAML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := testsupport.Execute(t, "dictionary", "regression-check", "--previous", path)
	if err == nil {
		t.Fatal("expected error when --candidate is missing")
	}
}
