// app/eventrulecycle/types_test.go
package eventrulecycle_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals/eventsignalstest"
)

// approvedType stores the type proposal name and approves it with window.
func (h *harness) approvedType(t *testing.T, name, window string) eventruleproposal.Proposal {
	t.Helper()
	ctx := context.Background()
	p, err := h.repo.Upsert(ctx, eventruleproposal.Proposal{
		Kind: eventruleproposal.KindType, TypeName: name, Description: name + " news.",
		Status: eventruleproposal.StatusPending, PromptVersion: eventrulecycle.EventRulesPromptVersion, CreatedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := p.ApproveType(window, false, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.TransitionType(ctx, approved, eventruleproposal.StatusPending); err != nil {
		t.Fatal(err)
	}
	return approved
}

// approvedPhrase stores an action phrase proposal and approves it.
func (h *harness) approvedPhrase(t *testing.T, target, phrase string, evidence ...string) eventruleproposal.Proposal {
	t.Helper()
	ctx := context.Background()
	p, err := h.repo.Upsert(ctx, eventruleproposal.Proposal{
		Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: target, Phrase: phrase,
		EvidenceIDs: evidence, Status: eventruleproposal.StatusPending, PromptVersion: eventrulecycle.EventRulesPromptVersion, CreatedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := eventrulecycle.Approve(ctx, h.deps, p, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func (h *harness) statusOfID(t *testing.T, id string) eventruleproposal.Status {
	t.Helper()
	p, err := h.repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

func (h *harness) applyApproved(t *testing.T) eventrulecycle.ApplyResult {
	t.Helper()
	ctx := context.Background()
	toApply, err := eventrulecycle.ApprovedNotApplied(ctx, h.deps)
	if err != nil {
		t.Fatal(err)
	}
	r, err := eventrulecycle.Apply(ctx, h.deps, toApply, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func idsOf(ps []eventruleproposal.Proposal) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestApply_TypeWithItsPhrase(t *testing.T) {
	h := newHarness(t)
	typ := h.approvedType(t, "kicking-error", "72h")
	phrase := h.approvedPhrase(t, "kicking-error", "misses a kick", "a3")

	r := h.applyApproved(t)

	want := eventrulecycle.AppliedType{
		Name: "kicking-error", Description: "kicking-error news.", Window: "72h",
		ProposalID: typ.ID, PromptVersion: eventrulecycle.EventRulesPromptVersion,
	}
	if len(h.gateIn) != 1 || len(h.gateIn[0].Types) != 1 || h.gateIn[0].Types[0] != want || phrasesOf(h.gateIn[0].Rules) != "ships off|misses a kick" {
		t.Fatalf("gate candidate %+v", h.gateIn)
	}
	if len(h.written) != 1 || len(h.written[0].Types) != 1 || h.written[0].Types[0] != want || phrasesOf(h.written[0].Rules) != "ships off|misses a kick" {
		t.Fatalf("written %+v", h.written)
	}
	if got := idsOf(r.Types); !slices.Equal(got, []string{typ.ID}) {
		t.Errorf("Types %v", got)
	}
	if got := idsOf(r.Applied); !slices.Equal(got, []string{phrase.ID}) {
		t.Errorf("Applied %v", got)
	}
	for _, id := range []string{typ.ID, phrase.ID} {
		if st := h.statusOfID(t, id); st != eventruleproposal.StatusApplied {
			t.Errorf("%s is %s, want applied", id, st)
		}
	}
}

func TestApply_TypeWithNoPhraseWaits(t *testing.T) {
	h := newHarness(t)
	typ := h.approvedType(t, "field-goal-miss", "")
	h.approvedPhrase(t, "injure", "lands on the shelf", "a2")

	r := h.applyApproved(t)

	if got := idsOf(r.Waiting); !slices.Equal(got, []string{typ.ID}) {
		t.Errorf("Waiting %v", got)
	}
	if len(r.Types) != 0 || len(h.written) != 1 || len(h.written[0].Types) != 0 {
		t.Fatalf("types %+v, written %+v", r.Types, h.written)
	}
	if st := h.statusOfID(t, typ.ID); st != eventruleproposal.StatusApproved {
		t.Errorf("type is %s, want approved", st)
	}
}

func TestApply_PhraseOfARejectedTypeIsAnOrphan(t *testing.T) {
	h := newHarness(t)
	typ := h.approvedType(t, "kicking-error", "")
	orphan := h.approvedPhrase(t, "kicking-error", "misses a kick", "a3")
	h.approvedPhrase(t, "injure", "lands on the shelf", "a2")
	rejected, err := typ.Reject(testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.Transition(context.Background(), rejected, eventruleproposal.StatusApproved); err != nil {
		t.Fatal(err)
	}

	r := h.applyApproved(t)

	if got := idsOf(r.Orphans); !slices.Equal(got, []string{orphan.ID}) {
		t.Errorf("Orphans %v", got)
	}
	if len(h.written) != 1 || phrasesOf(h.written[0].Rules) != "ships off|lands on the shelf" || len(h.written[0].Types) != 0 {
		t.Fatalf("written %+v", h.written)
	}
	if st := h.statusOfID(t, orphan.ID); st != eventruleproposal.StatusApproved {
		t.Errorf("orphan is %s, want approved", st)
	}
}

func TestApply_GateBlockedKeepsTypeAndPhraseApproved(t *testing.T) {
	h := newHarness(t)
	typ := h.approvedType(t, "kicking-error", "")
	phrase := h.approvedPhrase(t, "kicking-error", "misses a kick", "a3")
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "purity dropped"}

	r := h.applyApproved(t)

	if !r.Blocked || len(h.written) != 0 || len(r.Types) != 0 || len(r.Applied) != 0 {
		t.Fatalf("result %+v, written %+v", r, h.written)
	}
	if r.Candidate == nil || len(r.Candidate.Types) != 1 {
		t.Errorf("candidate %+v", r.Candidate)
	}
	for _, id := range []string{typ.ID, phrase.ID} {
		if st := h.statusOfID(t, id); st != eventruleproposal.StatusApproved {
			t.Errorf("%s is %s, want approved", id, st)
		}
	}
}

// reframeHarness has an analysis cue and a typed control article that is
// framing-only (typed analysis by that cue alone); the phrase "misses a
// kick" retypes it whatever action it targets.
func reframeHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.deps.BaseRules = func() (eventsignals.Rules, error) {
		return eventsignals.Rules{
			Taxonomy:    eventsignalstest.Taxonomy(),
			ContentCues: map[eventsignals.ContentSignal][]string{eventsignals.ContentSignalAnalysis: {"breakdown"}},
		}, nil
	}
	h.deps.Sampler = func(context.Context) ([]eventruleproposal.Sample, error) {
		s := testSample()
		s[2].Article.Title = "Kicker misses a kick late"
		return s, nil
	}
	h.deps.Control = func(context.Context) ([]eventruleproposal.Sample, error) {
		return []eventruleproposal.Sample{{Article: article.Article{ID: "t1", Title: "Breakdown: the kicker misses a kick again"}, ClusterID: "t1"}}, nil
	}
	return h
}

func TestAssess_ReframeAllowedForAnApprovedType(t *testing.T) {
	h := reframeHarness(t)
	h.approvedType(t, "kicking-error", "")
	c, err := eventrulecycle.Prepare(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !c.TypeTargets["kicking-error"] {
		t.Fatalf("TypeTargets %v", c.TypeTargets)
	}
	p := eventruleproposal.Proposal{Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: "kicking-error", Phrase: "misses a kick", EvidenceIDs: []string{"a3"}}
	a := eventrulecycle.Assess(h.deps, c, p, eventruleproposal.DefaultCheckOptions())
	if a.Impact.Reframed != 1 || !a.Verdict.OK {
		t.Fatalf("kicking-error: impact %+v, verdict %+v", a.Impact, a.Verdict)
	}
	p.Target = "sign"
	a = eventrulecycle.Assess(h.deps, c, p, eventruleproposal.DefaultCheckOptions())
	if a.Impact.Reframed != 1 || a.Verdict.OK || !slices.Contains(a.Verdict.Reasons, "retypes 1 framing-only articles") {
		t.Fatalf("sign: impact %+v, verdict %+v", a.Impact, a.Verdict)
	}
}

func TestDiscover_AllowedActionsIncludeApprovedTypes(t *testing.T) {
	h := newHarness(t)
	h.approvedType(t, "kicking-error", "")
	h.discovered(t)
	if !slices.Contains(h.llm.actions, "kicking-error") {
		t.Fatalf("allowed actions %v", h.llm.actions)
	}
}

func TestPrepare_TypeTargetsIncludeTheGeneratedFilesTypes(t *testing.T) {
	h := newHarness(t)
	h.existing.Types = []eventrulecycle.AppliedType{{Name: "field-goal-miss", Window: "48h", ProposalID: "evr_old"}}
	c, err := eventrulecycle.Prepare(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !c.TypeTargets["field-goal-miss"] || c.TypeTargets["sign"] {
		t.Fatalf("TypeTargets %v", c.TypeTargets)
	}
}

func TestRun_CountsApprovedTypesWaitingForPhrases(t *testing.T) {
	h := newHarness(t)
	h.approvedType(t, "field-goal-miss", "")
	_, out := h.run(t, "s\ns\n")
	if !strings.Contains(out, `1 approved type proposal(s) wait for phrases; add one with Add phrase in the panel or "events type add-phrase".`) {
		t.Errorf("no waiting count:\n%s", out)
	}
}

func TestPrintOrphansSaysTheTargetIsNeitherDeclaredNorApproved(t *testing.T) {
	var b strings.Builder
	eventrulecycle.PrintOrphans(&b, []eventruleproposal.Proposal{{ID: "evr_1", TargetKind: eventruleproposal.TargetAction, Target: "kicking-error", Phrase: "misses a kick"}})
	want := "1 approved phrase(s) have a target that is neither a declared action or signal nor an approved event type; they stay approved (reject them):\n" +
		"  - evr_1 action kicking-error \"misses a kick\"\n"
	if b.String() != want {
		t.Errorf("printOrphans =\n%q\nwant\n%q", b.String(), want)
	}
}
