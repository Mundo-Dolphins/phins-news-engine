package dictionaryminingfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryminingfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
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

func TestAddIgnoredTermAppendsToTheListKeepingComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mining.yml")
	src := "min_support: 2\n\n# never propose these\nignored_terms:\n  - alpha\n  # a comment inside\n  - beta\n\nignored_tokens:\n  - gamma\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := dictionaryminingfile.AddIgnoredTerm(path, "  Worst   Record ")
	if err != nil || !added {
		t.Fatalf("AddIgnoredTerm = %v, %v, want an append", added, err)
	}
	if again, _ := dictionaryminingfile.AddIgnoredTerm(path, "worst record"); again {
		t.Error("a term already listed must not be appended twice")
	}
	if again, _ := dictionaryminingfile.AddIgnoredTerm(path, "BETA"); again {
		t.Error("the check is case-insensitive")
	}
	got, _ := os.ReadFile(path)
	want := "min_support: 2\n\n# never propose these\nignored_terms:\n  - alpha\n  # a comment inside\n  - beta\n  - worst record\n\nignored_tokens:\n  - gamma\n"
	if string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	cfg, err := dictionaryminingfile.Load(path)
	if err != nil {
		t.Fatalf("Load() after append: %v", err)
	}
	if len(cfg.IgnoredTerms) != 3 || cfg.IgnoredTerms[2] != "worst record" {
		t.Errorf("IgnoredTerms = %v", cfg.IgnoredTerms)
	}
}

func TestLoadEnglishWordsAndNamesFromRoster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words")
	if err := os.WriteFile(path, []byte("Record\nbye\nNew York\nabc123\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	words, err := dictionaryminingfile.LoadEnglishWords(path)
	if err != nil || !words["record"] || !words["bye"] || len(words) != 2 {
		t.Fatalf("LoadEnglishWords = %v, %v, want {record, bye}", words, err)
	}
	names, surnames := dictionaryminingfile.NamesFromRoster([]nflverse.Player{
		{FullName: "Marvin Harrison Jr.", LastName: "Harrison", FootballName: "Marvin"},
		{FullName: "Ja'Marr Chase", LastName: "Chase"},
	})
	if !names["marvin harrison"] || !names["ja'marr chase"] || !surnames["harrison"] || !surnames["chase"] {
		t.Errorf("names = %v surnames = %v", names, surnames)
	}
}
