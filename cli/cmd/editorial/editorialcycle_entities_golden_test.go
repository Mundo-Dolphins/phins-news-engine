package editorial_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// entityStepTranscript runs a cycle whose single article is titled title,
// answering stdin, and returns the entity step's output: from the line after
// the article review ends to the event rules step (or, when the cycle stops,
// the audit report line), exclusive.
func entityStepTranscript(t *testing.T, llm *fakeEntityLLM, title, stdin string) string {
	t.Helper()
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusOK, &hits))
	useFakeEntityLLM(t, llm)
	seedTitled(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "g1", title)
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, stdin, args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	start := strings.Index(out, "review complete\n")
	end := strings.Index(out, "event rules:")
	if end < 0 {
		end = strings.Index(out, "audit report:")
	}
	if start < 0 || end < start {
		t.Fatalf("no entity step section in output:\n%s", out)
	}
	return out[start+len("review complete\n") : end]
}

func TestEntityStepGolden_ApproveAndRegenerate(t *testing.T) {
	llm := &fakeEntityLLM{entities: []appout.EntityDiscoveryEntity{{
		Name: "Brandon Newcomer", Type: "player", Confidence: 0.9, SourceArticleIDs: []string{"g1"},
		Metadata: map[string]string{"team": "Springfield Isotopes", "team_id": "springfield-isotopes", "position": "CB"},
	}}}
	got := entityStepTranscript(t, llm, "Isotopes sign Brandon Newcomer", "p\na\n")
	want := "entities: 1 articles, 1 batches (0 failed), 1 proposals created or updated\n" +
		"\n--- Entity proposal 1/1 ---\n" +
		"Brandon Newcomer · player · new_entity · confidence 0.90 · occurrences 1\n" +
		"team: Springfield Isotopes\n" +
		"position: CB\n" +
		"Source: Isotopes sign Brandon Newcomer\n" +
		"Action: [a]pprove, [r]eject, [s]kip, [q]uit: Approved Brandon Newcomer.\n" +
		"Entity review complete. approved: 1, rejected: 0, skipped: 0\n" +
		"entity catalog regenerated\n"
	if got != want {
		t.Errorf("entity step =\n%q\nwant\n%q", got, want)
	}
}

func TestEntityStepGolden_QuitPausesTheReview(t *testing.T) {
	llm := &fakeEntityLLM{entities: []appout.EntityDiscoveryEntity{{Name: "Casey Newcomer", Type: "player", Confidence: 0.9, SourceArticleIDs: []string{"g1"}}}}
	got := entityStepTranscript(t, llm, "Isotopes sign Casey Newcomer", "p\nx\nq\n")
	want := "entities: 1 articles, 1 batches (0 failed), 1 proposals created or updated\n" +
		"\n--- Entity proposal 1/1 ---\n" +
		"Casey Newcomer · player · new_entity · confidence 0.90 · occurrences 1\n" +
		"Source: Isotopes sign Casey Newcomer\n" +
		"Action: [a]pprove, [r]eject, [s]kip, [q]uit: Unknown action. Choose a, r, s, or q.\n" +
		"Action: [a]pprove, [r]eject, [s]kip, [q]uit: Entity review paused. approved: 0, rejected: 0, skipped: 0\n" +
		"editorial cycle stopped — entity proposals saved as pending; finish them with `phins-news-engine-tools entities review`, then run the dictionary update, training and deployment again (the review session is already completed, so --resume does not re-enter this step)\n"
	if got != want {
		t.Errorf("entity step =\n%q\nwant\n%q", got, want)
	}
}

func TestEntityStepGolden_DiscoveryUnavailable(t *testing.T) {
	llm := &fakeEntityLLM{err: fmt.Errorf("connection refused")}
	got := entityStepTranscript(t, llm, "Isotopes sign Dana Newcomer", "p\n")
	want := "entities: 1 articles, 1 batches (1 failed), 0 proposals created or updated\n" +
		"WARNING: entity discovery is unavailable (batch 1 (articles: g1): connection refused); no new entities this cycle. The articles stay unprocessed and a later run retries them.\n" +
		"No pending entity proposals for this session.\n"
	if got != want {
		t.Errorf("entity step =\n%q\nwant\n%q", got, want)
	}
}

func TestEntityStepGolden_NoReviewedArticles(t *testing.T) {
	llm := &fakeEntityLLM{}
	got := entityStepTranscript(t, llm, "Isotopes sign Ellis Newcomer", "k\n")
	want := "entities: the session has no reviewed articles\n"
	if got != want {
		t.Errorf("entity step =\n%q\nwant\n%q", got, want)
	}
	if llm.calls != 0 {
		t.Errorf("LLM calls = %d, want 0 when the session reviewed nothing", llm.calls)
	}
}
