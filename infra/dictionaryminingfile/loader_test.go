package dictionaryminingfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryminingfile"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dictionary_mining.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writePolicy(t, `
min_support: 2
ignored_terms:
  - game
  - press conference
ignored_tokens:
  - preview
ignored_substrings:
  - subscribe
category_guesses:
  Brockway Badgers: rivals
`)
	got, err := dictionaryminingfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.MinSupport != 2 || len(got.IgnoredTerms) != 2 || len(got.IgnoredTokens) != 1 || len(got.IgnoredSubstrings) != 1 {
		t.Errorf("Load() = %+v, want parsed filter config", got)
	}
	if got.CategoryGuesses["brockway badgers"] != "rivals" {
		t.Errorf("CategoryGuesses = %+v, want normalized Brockway Badgers override", got.CategoryGuesses)
	}
}

func TestLoad_RejectsUnknownFields(t *testing.T) {
	path := writePolicy(t, `
min_support: 2
ignored_terms: []
bogus: true
`)
	if _, err := dictionaryminingfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for unknown field")
	}
}

func TestLoad_RejectsNegativeMinSupport(t *testing.T) {
	path := writePolicy(t, `
min_support: -1
ignored_terms: []
`)
	if _, err := dictionaryminingfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for negative min_support")
	}
}

func TestExamplePolicy_FiltersCommonWordsAndKeepsEntities(t *testing.T) {
	policy, err := dictionaryminingfile.Load(repofs.ExampleConfig(t, "dictionary_mining.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"start", "going", "despite", "fans", "about", "they", "Keep Going", "Improvement Despite", "Gave Fans", "lost their", "This Week"} {
		if policy.Allows(term, 20) {
			t.Errorf("common word or phrase %q remains eligible", term)
		}
	}
	for _, term := range []string{"Stubbs", "Jordan Sample", "Shelbyville", "Ogdenville Owls", "Casey Fixture", "Morgan Mockley", "Ledger"} {
		if !policy.Allows(term, 2) {
			t.Errorf("entity %q was filtered", term)
		}
	}
}
