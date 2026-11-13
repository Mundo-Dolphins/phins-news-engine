package dictionary_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

// minedTermsGoldenRun runs the interactive mined-term review of the fixture
// session with stdin and returns the output with the temp dir replaced by
// "$DIR", so the golden transcripts below are stable. A non-empty policy
// replaces the fixture's mining policy file.
func minedTermsGoldenRun(t *testing.T, policy, stdin string) string {
	t.Helper()
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dir := filepath.Dir(configPath)
	if policy != "" {
		if err := os.WriteFile(filepath.Join(dir, "dictionary_mining.yml"), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, filepath.Join(dir, "operational.db"))
	out, err := testsupport.ExecuteWithStdin(t, stdin, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, out)
	}
	return strings.ReplaceAll(out, dir, "$DIR")
}

const minedTermsHeader = "session sess-1: 11 candidate term(s) mined\n" +
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
	"  camp session                   support=1 guess=\n"

const minedTermsCategories = "Categories:\n" +
	"  1. coaching_staff\n" +
	"  2. nfl_people_context\n" +
	"  3. official_names\n" +
	"category [number or name]: "

func minedTermScreen(term, support, guess string) string {
	return "----------------------------------------------------------------\n" +
		"Term:      " + term + "\n" +
		"Support:   " + support + " (first seen: a1)\n" +
		"Guess:     " + guess + "\n" +
		"[a]pprove [r]eject [c]ategorize [q]uit-and-save-progress\n> "
}

// TestMinedTermsGolden_ApproveRejectCategorizeQuit pins the exact transcript
// of the mined-term loop: approve with a known guess, approve with no guess
// (category asked), reject (ignored_terms line), categorize, an unknown
// action and quit.
func TestMinedTermsGolden_ApproveRejectCategorizeQuit(t *testing.T) {
	got := minedTermsGoldenRun(t, "", "a\na\n2\nr\nc\ncoaching_staff\nz\nq\n")
	want := minedTermsHeader +
		minedTermScreen("Ezo Quarrington", "2", "nfl_people_context") +
		minedTermScreen("camp", "1", "") + minedTermsCategories +
		minedTermScreen("camp session", "1", "") +
		"WARNING: rejected term not added to $DIR/dictionary_mining.yml: dictionaryminingfile: $DIR/dictionary_mining.yml has no ignored_terms list\n" +
		minedTermScreen("ezo", "1", "") + minedTermsCategories +
		minedTermScreen("leads", "1", "") +
		"unknown action \"z\" — use one of [a r c q]\n" +
		minedTermScreen("leads training", "1", "") +
		"progress saved — resume with `dictionary mine-candidates --session sess-1`\n" +
		"dictionary updated: 3 entries added, version 1.0.0 -> 1.0.1\n"
	if got != want {
		t.Errorf("output =\n%s\nwant\n%s", got, want)
	}
}

// TestMinedTermsGolden_RejectToTheEnd pins the transcript of a loop that runs
// to the end of the offered terms: rejected terms are added to a block-style
// ignored_terms list and the one approval is written once the loop ends.
func TestMinedTermsGolden_RejectToTheEnd(t *testing.T) {
	policy := "min_support: 1\nignored_terms:\n  - zzz\nignored_tokens: []\nignored_substrings: []\ncategory_guesses: {}\n"
	got := minedTermsGoldenRun(t, policy, "a\n"+strings.Repeat("r\n", 10))
	var want strings.Builder
	want.WriteString(minedTermsHeader)
	want.WriteString(minedTermScreen("Ezo Quarrington", "2", "nfl_people_context"))
	for _, term := range []string{"camp", "camp session", "ezo", "leads", "leads training", "quarrington", "quarrington leads", "session", "training", "training camp"} {
		want.WriteString(minedTermScreen(term, "1", ""))
		want.WriteString("  added \"" + term + "\" to ignored_terms in $DIR/dictionary_mining.yml\n")
	}
	want.WriteString("dictionary updated: 1 entry added, version 1.0.0 -> 1.0.1\n")
	if got != want.String() {
		t.Errorf("output =\n%s\nwant\n%s", got, want.String())
	}
}
