// app/eventrulecycle/steps_test.go
package eventrulecycle_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// discovered runs Prepare and Discover on the harness and returns the
// context; the harness LLM proposes "lands on the shelf", "inks" (fails the
// checks) and "inks a deal".
func (h *harness) discovered(t *testing.T) eventrulecycle.Context {
	t.Helper()
	ctx := context.Background()
	c, err := eventrulecycle.Prepare(ctx, h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventrulecycle.Discover(ctx, h.deps, c, 4); err != nil {
		t.Fatal(err)
	}
	return c
}

func (h *harness) proposal(t *testing.T, phrase string) eventruleproposal.Proposal {
	t.Helper()
	p, ok := h.byPhrase(t)[phrase]
	if !ok {
		t.Fatalf("no proposal %q", phrase)
	}
	return p
}

func TestPrepareEmptySample(t *testing.T) {
	h := newHarness(t)
	h.deps.Sampler = func(context.Context) ([]eventruleproposal.Sample, error) { return nil, nil }
	h.deps.BaseRules = nil // never reached
	c, err := eventrulecycle.Prepare(context.Background(), h.deps)
	if err != nil || len(c.Sample) != 0 {
		t.Fatalf("context %+v, err %v", c, err)
	}
}

func TestDiscoverCountsAndStores(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, err := eventrulecycle.Prepare(ctx, h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sample) != 10 || c.Text["a1"] != "isotopes inks a deal with veteran contract news." {
		t.Fatalf("context %+v", c)
	}
	r, err := eventrulecycle.Discover(ctx, h.deps, c, 4)
	if err != nil {
		t.Fatal(err)
	}
	if r.Sampled != 10 || r.Calls != 6 || r.Failed != 0 || r.Proposed != 4 || r.Unavailable || r.FirstErr != nil {
		t.Fatalf("result %+v", r)
	}
}

func TestDiscoverUnavailable(t *testing.T) {
	h := newHarness(t)
	h.llm.topicErr = errors.New("down")
	h.llm.phraseErr = errors.New("down")
	ctx := context.Background()
	c, err := eventrulecycle.Prepare(ctx, h.deps)
	if err != nil {
		t.Fatal(err)
	}
	r, err := eventrulecycle.Discover(ctx, h.deps, c, 4)
	if err != nil || !r.Unavailable || r.Failed != 6 || r.Proposed != 0 || r.FirstErr == nil {
		t.Fatalf("result %+v, err %v", r, err)
	}
	if len(h.byPhrase(t)) != 0 {
		t.Error("proposals stored after an outage")
	}
}

func TestAssessFlagsFailing(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	as, err := eventrulecycle.Pending(context.Background(), h.deps, c, eventruleproposal.DefaultCheckOptions())
	if err != nil {
		t.Fatal(err)
	}
	var phrases []string
	var inks eventrulecycle.Assessment
	for _, a := range as {
		phrases = append(phrases, a.Proposal.Phrase)
		if a.Proposal.Phrase == "inks" {
			inks = a
		}
	}
	if strings.Join(phrases, "|") != "lands on the shelf|inks|inks a deal" {
		t.Fatalf("pending %v", phrases)
	}
	if inks.Verdict.OK || inks.NoEvidence || !strings.Contains(strings.Join(inks.Verdict.Reasons, "; "), "phrase shorter than") {
		t.Errorf("failing assessment %+v", inks)
	}
	if got := statusOf(t, h, "inks"); got != eventruleproposal.StatusPending {
		t.Errorf("failing proposal was decided: %s", got)
	}
	if a := as[0]; !a.Verdict.OK || a.Impact.Moved["injury"] != 1 {
		t.Errorf("passing assessment %+v", a)
	}
}

func TestAssessNoEvidence(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	c.Sample = c.Sample[2:] // drop a1 and a2
	a := eventrulecycle.Assess(h.deps, c, h.proposal(t, "lands on the shelf"), eventruleproposal.DefaultCheckOptions())
	if !a.NoEvidence || a.Verdict.OK {
		t.Fatalf("assessment %+v", a)
	}
}

func TestAssessNormalisesPhrase(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	p := h.proposal(t, "lands on the shelf")
	p.Phrase = "  Lands On The Shelf "
	if a := eventrulecycle.Assess(h.deps, c, p, eventruleproposal.DefaultCheckOptions()); a.Proposal.Phrase != "lands on the shelf" || !a.Verdict.OK {
		t.Fatalf("assessment %+v", a)
	}
}

func TestEditPreviewWritesNothing(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	before := h.byPhrase(t)
	orig := before["lands on the shelf"]
	a, err := eventrulecycle.Edit(context.Background(), h.deps, c, orig, " Lands On The Shelf With ", eventruleproposal.DefaultCheckOptions(), testNow, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Proposal.Phrase != "lands on the shelf with" || !a.Verdict.OK || a.Impact.Moved["injury"] != 1 || a.Proposal.ID != "" {
		t.Fatalf("preview %+v", a)
	}
	after := h.byPhrase(t)
	if len(after) != len(before) || after["lands on the shelf"].Status != eventruleproposal.StatusPending {
		t.Errorf("preview wrote: %+v", after)
	}
}

func TestEditSaveApprovesEditedRejectsOriginal(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	orig := h.proposal(t, "lands on the shelf")
	a, err := eventrulecycle.Edit(context.Background(), h.deps, c, orig, "lands on the shelf with", eventruleproposal.DefaultCheckOptions(), testNow, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.Proposal.ID == "" || a.Proposal.Status != eventruleproposal.StatusApproved || a.Proposal.Phrase != "lands on the shelf with" ||
		strings.Join(a.Proposal.EvidenceIDs, ",") != "a2" {
		t.Fatalf("edited %+v", a.Proposal)
	}
	if got := statusOf(t, h, "lands on the shelf with"); got != eventruleproposal.StatusApproved {
		t.Errorf("edited stored %s", got)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusRejected {
		t.Errorf("original %s", got)
	}
}

func TestEditUnchanged(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	for _, phrase := range []string{" Lands On The Shelf", "   "} {
		if _, err := eventrulecycle.Edit(context.Background(), h.deps, c, h.proposal(t, "lands on the shelf"), phrase, eventruleproposal.DefaultCheckOptions(), testNow, true); !errors.Is(err, eventrulecycle.ErrPhraseUnchanged) {
			t.Errorf("%q: err %v", phrase, err)
		}
	}
}

func TestEditNoCitedEvidence(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	_, err := eventrulecycle.Edit(context.Background(), h.deps, c, h.proposal(t, "lands on the shelf"), "inks a deal", eventruleproposal.DefaultCheckOptions(), testNow, true)
	if !errors.Is(err, eventrulecycle.ErrNoCitedEvidence) || !strings.Contains(err.Error(), `"inks a deal"`) {
		t.Fatalf("err %v", err)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusPending {
		t.Errorf("original %s", got)
	}
}

func TestEditFailsChecks(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	a, err := eventrulecycle.Edit(context.Background(), h.deps, c, h.proposal(t, "lands on the shelf"), "shelf", eventruleproposal.DefaultCheckOptions(), testNow, true)
	if !errors.Is(err, eventrulecycle.ErrEditFailsChecks) || !strings.Contains(err.Error(), "phrase shorter than") {
		t.Fatalf("err %v", err)
	}
	if a.Verdict.OK || len(a.Verdict.Reasons) == 0 {
		t.Errorf("assessment %+v", a)
	}
	if _, stored := h.byPhrase(t)["shelf"]; stored {
		t.Error("failing edit stored")
	}
}

func TestEditOntoDecidedPhraseIsAlreadyDecided(t *testing.T) {
	h := newHarness(t)
	c := h.discovered(t)
	ctx := context.Background()
	if _, err := eventrulecycle.Reject(ctx, h.deps, h.proposal(t, "inks a deal"), testNow); err != nil {
		t.Fatal(err)
	}
	orig := h.proposal(t, "inks")
	_, err := eventrulecycle.Edit(ctx, h.deps, c, orig, "inks a deal", eventruleproposal.DefaultCheckOptions(), testNow, true)
	if !errors.Is(err, eventrulecycle.ErrAlreadyDecided) {
		t.Fatalf("err %v", err)
	}
	if got := statusOf(t, h, "inks"); got != eventruleproposal.StatusPending {
		t.Errorf("original %s", got)
	}
}

func TestApproveRejectAlreadyDecided(t *testing.T) {
	h := newHarness(t)
	h.discovered(t)
	ctx := context.Background()
	stale := h.proposal(t, "inks a deal")
	approved, err := eventrulecycle.Approve(ctx, h.deps, stale, testNow)
	if err != nil || approved.Status != eventruleproposal.StatusApproved {
		t.Fatalf("approve %+v, %v", approved, err)
	}
	// stale still says pending; the stored row is approved.
	if _, err := eventrulecycle.Approve(ctx, h.deps, stale, testNow); !errors.Is(err, eventrulecycle.ErrAlreadyDecided) {
		t.Errorf("second approve err %v", err)
	}
	got, err := eventrulecycle.ApprovedNotApplied(ctx, h.deps)
	if err != nil || len(got) != 1 || got[0].ID != stale.ID {
		t.Fatalf("approved %+v, %v", got, err)
	}
	if _, err := eventrulecycle.Reject(ctx, h.deps, stale, testNow); err != nil {
		t.Errorf("reject approved: %v", err)
	}
	if _, err := eventrulecycle.Reject(ctx, h.deps, stale, testNow); !errors.Is(err, eventrulecycle.ErrAlreadyDecided) {
		t.Errorf("second reject err %v", err)
	}
}

func TestApplyBlockedLeavesApproved(t *testing.T) {
	h := newHarness(t)
	h.discovered(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "generated.yml")
	original := []byte("action_terms: {}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	h.deps.GeneratedPath = path
	h.deps.ReadApplied = func(string) ([]eventrulecycle.AppliedRule, error) { return nil, nil }
	h.deps.WriteApplied = func(p string, _ []eventrulecycle.AppliedRule) error {
		return os.WriteFile(p, []byte("changed\n"), 0o600)
	}
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "purity dropped", Affected: 2}
	approved, err := eventrulecycle.Approve(ctx, h.deps, h.proposal(t, "lands on the shelf"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	r, err := eventrulecycle.Apply(ctx, h.deps, []eventruleproposal.Proposal{approved}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Blocked || r.Reason != "purity dropped" || r.Nothing || len(r.Applied) != 0 || r.Gate.Affected != 2 || phrasesOf(r.Candidate) != "lands on the shelf" {
		t.Fatalf("result %+v", r)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, original) {
		t.Errorf("file changed: %q", b)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusApproved {
		t.Errorf("status %s", got)
	}
}

func TestApplyPassWritesAndMarksApplied(t *testing.T) {
	h := newHarness(t)
	h.discovered(t)
	ctx := context.Background()
	approved, err := eventrulecycle.Approve(ctx, h.deps, h.proposal(t, "lands on the shelf"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	r, err := eventrulecycle.Apply(ctx, h.deps, []eventruleproposal.Proposal{approved}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.Blocked || r.Nothing || len(r.Applied) != 1 || r.Applied[0].Status != eventruleproposal.StatusApplied {
		t.Fatalf("result %+v", r)
	}
	if len(h.written) != 1 || phrasesOf(h.written[0]) != "ships off|lands on the shelf" {
		t.Errorf("written %+v", h.written)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusApplied {
		t.Errorf("status %s", got)
	}
}

func TestApplyNothing(t *testing.T) {
	h := newHarness(t)
	r, err := eventrulecycle.Apply(context.Background(), h.deps, nil, testNow)
	if err != nil || !r.Nothing || r.Blocked || len(r.Applied) != 0 {
		t.Fatalf("result %+v, err %v", r, err)
	}
	if len(h.gateIn) != 0 || len(h.written) != 0 {
		t.Errorf("gate %d, writes %d", len(h.gateIn), len(h.written))
	}
}
