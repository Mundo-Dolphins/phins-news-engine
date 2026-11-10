// app/eventrulecycle/golden_test.go
package eventrulecycle_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
)

// Exact output of full review+apply sessions, pinned before Run was split
// into non-interactive steps so the split cannot change a byte.

// redactIDs replaces every stored proposal ID with <phrase> placeholders.
func redactIDs(t *testing.T, h *harness, out string) string {
	t.Helper()
	var pairs []string
	for key, p := range h.byPhrase(t) {
		pairs = append(pairs, p.ID, "<"+key+">")
	}
	return strings.NewReplacer(pairs...).Replace(out)
}

func TestRun_GoldenReviewEditApply(t *testing.T) {
	h := newHarness(t)
	_, out := h.run(t, "x\ne\nshelf\ne\nlands on the shelf\ne\n  Lands On The Shelf With \nr\n")
	out = redactIDs(t, h, out)
	want := goldenReviewEditApply
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s\nquoted:\n%q", out, want, out)
	}
}

func TestRun_GoldenGateBlockedThenCarriedOver(t *testing.T) {
	h := newHarness(t)
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "purity dropped from 0.9000 to 0.8000", Affected: 2}
	_, first := h.run(t, "a\na\n")
	h.gate = eventrulecycle.GateResult{OK: true, Affected: 1}
	_, second := h.run(t, "x\nr\nk\n")
	out := redactIDs(t, h, first+"=== second run ===\n"+second)
	want := goldenGateBlockedThenCarriedOver
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s\nquoted:\n%q", out, want, out)
	}
}

func TestRun_GoldenQuitDuringCarriedOver(t *testing.T) {
	h := newHarness(t)
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "blocked"}
	h.run(t, "a\nr\n")
	_, out := h.run(t, "q\n")
	out = redactIDs(t, h, out)
	want := goldenQuitDuringCarriedOver
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s\nquoted:\n%q", out, want, out)
	}
}

const goldenReviewEditApply = "event rules: 10 generic articles sampled, 6 LLM calls (0 failed)\n" +
	"event rules: 4 proposals created or updated\n" +
	"\n" +
	"--- Event rule proposal 1/3 ---\n" +
	"action injure · phrase \"lands on the shelf\" · evidence a2\n" +
	"Impact: 1 generic → injury\n" +
	"  e.g. Ezo lands on the shelf with ankle\n" +
	"Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: Unknown action. Choose a, r, e, s, or q.\n" +
	"Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: New phrase: Edited phrase \"shelf\" fails the checks: phrase shorter than 6 characters; phrase has fewer than 2 tokens and is not a distinctive single word\n" +
	"Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: New phrase: Phrase unchanged; choose another action.\n" +
	"Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: New phrase: Impact: 1 generic → injury\n" +
	"  e.g. Ezo lands on the shelf with ankle\n" +
	"Approved edited phrase \"lands on the shelf with\" (original \"lands on the shelf\" superseded).\n" +
	"\n" +
	"--- Event rule proposal 2/3 ---\n" +
	"action sign · \"inks\" auto-rejected: phrase shorter than 6 characters; phrase has fewer than 2 tokens and is not a distinctive single word\n" +
	"\n" +
	"--- Event rule proposal 3/3 ---\n" +
	"action sign · phrase \"inks a deal\" · evidence a1\n" +
	"Impact: 1 generic → transaction\n" +
	"  e.g. Isotopes inks a deal with veteran\n" +
	"Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: Rejected \"inks a deal\".\n" +
	"Event rule review complete. approved: 1, rejected: 2, skipped: 0\n" +
	"Clustering gate: gate not exercised (0 dataset articles affected)\n" +
	"Clustering gate passed; 1 rule(s) applied to generated.yaml\n" +
	"Event type proposals from this run (not applied automatically; decide with `events type approve|reject <id>`):\n" +
	"  - <type:stadium renovation> stadium renovation: work on the stadium (evidence a3)\n"

const goldenGateBlockedThenCarriedOver = "event rules: 10 generic articles sampled, 6 LLM calls (0 failed)\n" +
	"event rules: 4 proposals created or updated\n" +
	"\n" +
	"--- Event rule proposal 1/3 ---\n" +
	"action injure · phrase \"lands on the shelf\" · evidence a2\n" +
	"Impact: 1 generic → injury\n" +
	"  e.g. Ezo lands on the shelf with ankle\n" +
	"Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: Approved \"lands on the shelf\".\n" +
	"\n" +
	"--- Event rule proposal 2/3 ---\n" +
	"action sign · \"inks\" auto-rejected: phrase shorter than 6 characters; phrase has fewer than 2 tokens and is not a distinctive single word\n" +
	"\n" +
	"--- Event rule proposal 3/3 ---\n" +
	"action sign · phrase \"inks a deal\" · evidence a1\n" +
	"Impact: 1 generic → transaction\n" +
	"  e.g. Isotopes inks a deal with veteran\n" +
	"Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: Approved \"inks a deal\".\n" +
	"Event rule review complete. approved: 2, rejected: 1, skipped: 0\n" +
	"Clustering gate: 2 dataset article(s) affected\n" +
	"Clustering gate blocked 2 approved rule(s): purity dropped from 0.9000 to 0.8000\n" +
	"  - <lands on the shelf> action injure \"lands on the shelf\"\n" +
	"  - <inks a deal> action sign \"inks a deal\"\n" +
	"They stay approved; no rules file was written. Reject the offender in the next run.\n" +
	"Event type proposals from this run (not applied automatically; decide with `events type approve|reject <id>`):\n" +
	"  - <type:stadium renovation> stadium renovation: work on the stadium (evidence a3)\n" +
	"=== second run ===\n" +
	"event rules: 10 generic articles sampled, 6 LLM calls (0 failed)\n" +
	"event rules: 0 proposals created or updated\n" +
	"No pending event rule phrase proposals.\n" +
	"\n" +
	"--- Event rule 1/2: approved, not yet applied ---\n" +
	"<lands on the shelf> · action injure · phrase \"lands on the shelf\" · evidence a2\n" +
	"Impact: 1 generic → injury\n" +
	"  e.g. Ezo lands on the shelf with ankle\n" +
	"Action: [k]eep for this apply, [r]eject, [q]uit: Unknown action. Choose k, r, or q.\n" +
	"Action: [k]eep for this apply, [r]eject, [q]uit: Rejected \"lands on the shelf\".\n" +
	"\n" +
	"--- Event rule 2/2: approved, not yet applied ---\n" +
	"<inks a deal> · action sign · phrase \"inks a deal\" · evidence a1\n" +
	"Impact: 1 generic → transaction\n" +
	"  e.g. Isotopes inks a deal with veteran\n" +
	"Action: [k]eep for this apply, [r]eject, [q]uit: Clustering gate: 1 dataset article(s) affected\n" +
	"Clustering gate passed; 1 rule(s) applied to generated.yaml\n" +
	"1 older pending type proposal(s) not shown; see `events type list`.\n"

const goldenQuitDuringCarriedOver = "event rules: 10 generic articles sampled, 6 LLM calls (0 failed)\n" +
	"event rules: 0 proposals created or updated\n" +
	"No pending event rule phrase proposals.\n" +
	"\n" +
	"--- Event rule 1/1: approved, not yet applied ---\n" +
	"<lands on the shelf> · action injure · phrase \"lands on the shelf\" · evidence a2\n" +
	"Impact: 1 generic → injury\n" +
	"  e.g. Ezo lands on the shelf with ankle\n" +
	"Action: [k]eep for this apply, [r]eject, [q]uit: \n" +
	"Event rule review paused. approved: 0, rejected: 0, skipped: 0\n" +
	"1 older pending type proposal(s) not shown; see `events type list`.\n"
