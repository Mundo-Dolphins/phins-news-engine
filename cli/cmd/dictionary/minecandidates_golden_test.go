package dictionary_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

// TestMineCandidatesDryRunOutput pins the exact `dictionary mine-candidates
// --dry-run` output, including the roster and English word-list WARNING lines
// printed when those optional inputs are configured but missing.
func TestMineCandidatesDryRunOutput(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dir := filepath.Dir(configPath)
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, filepath.Join(dir, "operational.db"))

	rosterPath := filepath.Join(dir, "missing-roster.csv")
	wordsPath := filepath.Join(dir, "missing-words.txt")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configText := strings.Replace(string(configBytes), "clustering:\n", fmt.Sprintf("clustering:\n  entity_catalog:\n    league:\n      roster_path: %s\n", rosterPath), 1)
	configText = strings.Replace(configText, "dictionary_mining:\n", fmt.Sprintf("dictionary_mining:\n  english_words_path: %s\n", wordsPath), 1)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := testsupport.Execute(t, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID, "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, out)
	}
	want := fmt.Sprintf("WARNING: nflverse roster %s not found; people it covers are not filtered out\n", rosterPath) +
		fmt.Sprintf("WARNING: english word list unavailable (dictionaryminingfile: open %s: open %s: no such file or directory); coined words (nicknames, slang) are not mined\n", wordsPath, wordsPath) +
		"session sess-1: 11 candidate term(s) mined\n" +
		"  Ezo Quarrington                support=2 guess=nfl_people_context\n" +
		"  ezo                            support=1 guess=\n" +
		"  quarrington                    support=1 guess=\n" +
		"  leads                          support=1 guess=\n" +
		"  training                       support=1 guess=\n" +
		"  camp                           support=1 guess=\n" +
		"  session                        support=1 guess=\n" +
		"  quarrington leads              support=1 guess=\n" +
		"  leads training                 support=1 guess=\n" +
		"  training camp                  support=1 guess=\n" +
		"  camp session                   support=1 guess=\n" +
		"dry run: no candidates persisted, dictionary not modified\n"
	if out != want {
		t.Errorf("output =\n%q\nwant\n%q", out, want)
	}
}
