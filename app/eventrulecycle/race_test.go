// app/eventrulecycle/race_test.go
package eventrulecycle_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// staleGetRepo returns every proposal as pending from Get, as a reader that
// loaded it before another decision landed.
type staleGetRepo struct {
	*sqliteeventruleproposal.Repository
}

func (r staleGetRepo) Get(ctx context.Context, id string) (eventruleproposal.Proposal, error) {
	p, err := r.Repository.Get(ctx, id)
	p.Status = eventruleproposal.StatusPending
	return p, err
}

func TestDecideLosingARaceIsAlreadyDecidedAndKeepsTheOtherDecision(t *testing.T) {
	h := newHarness(t)
	h.discovered(t)
	ctx := context.Background()
	p := h.proposal(t, "inks a deal")
	if _, err := eventrulecycle.Approve(ctx, h.deps, p, testNow); err != nil {
		t.Fatal(err)
	}
	h.deps.Proposals = staleGetRepo{h.repo}
	if _, err := eventrulecycle.Reject(ctx, h.deps, p, testNow); !errors.Is(err, eventrulecycle.ErrAlreadyDecided) {
		t.Fatalf("reject err %v, want ErrAlreadyDecided", err)
	}
	if got := statusOf(t, h, "inks a deal"); got != eventruleproposal.StatusApproved {
		t.Errorf("status %s, want approved", got)
	}
}

func TestRejectFromOnlyRejectsTheExpectedStatus(t *testing.T) {
	h := newHarness(t)
	h.discovered(t)
	ctx := context.Background()
	p := h.proposal(t, "inks a deal")
	if _, err := eventrulecycle.Approve(ctx, h.deps, p, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := eventrulecycle.RejectFrom(ctx, h.deps, p, eventruleproposal.StatusPending, testNow); !errors.Is(err, eventrulecycle.ErrAlreadyDecided) {
		t.Fatalf("reject from pending err %v, want ErrAlreadyDecided", err)
	}
	if got := statusOf(t, h, "inks a deal"); got != eventruleproposal.StatusApproved {
		t.Fatalf("status %s, want approved", got)
	}
	rejected, err := eventrulecycle.RejectFrom(ctx, h.deps, p, eventruleproposal.StatusApproved, testNow)
	if err != nil || rejected.Status != eventruleproposal.StatusRejected {
		t.Fatalf("reject from approved %+v, %v", rejected, err)
	}
}

func TestApplyStaleWhenAProposalIsRejectedDuringTheGate(t *testing.T) {
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
	shelf, err := eventrulecycle.Approve(ctx, h.deps, h.proposal(t, "lands on the shelf"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	deal, err := eventrulecycle.Approve(ctx, h.deps, h.proposal(t, "inks a deal"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Gate = func(context.Context, []eventrulecycle.AppliedRule) (eventrulecycle.GateResult, error) {
		// The owner rejects one rule while the gate runs.
		if _, err := eventrulecycle.Reject(ctx, h.deps, deal, testNow); err != nil {
			t.Fatal(err)
		}
		return eventrulecycle.GateResult{OK: true}, nil
	}
	r, err := eventrulecycle.Apply(ctx, h.deps, []eventruleproposal.Proposal{shelf, deal}, testNow)
	if !errors.Is(err, eventrulecycle.ErrApplyStale) || err.Error() != "proposals changed during the gate; run apply again" {
		t.Fatalf("err %v, want ErrApplyStale", err)
	}
	if len(r.Applied) != 0 {
		t.Errorf("applied %+v", r.Applied)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, original) {
		t.Errorf("file changed: %q", b)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusApproved {
		t.Errorf("shelf %s, want approved", got)
	}
	if got := statusOf(t, h, "inks a deal"); got != eventruleproposal.StatusRejected {
		t.Errorf("deal %s, want rejected", got)
	}
}

// failMarkRepo fails every save to applied, as a database lost after the
// rules file was written.
type failMarkRepo struct {
	*sqliteeventruleproposal.Repository
}

func (r failMarkRepo) Transition(ctx context.Context, p eventruleproposal.Proposal, from eventruleproposal.Status) error {
	if p.Status == eventruleproposal.StatusApplied {
		return errors.New("database is locked")
	}
	return r.Repository.Transition(ctx, p, from)
}

func TestApplyMarkFailureSaysTheFileWasWritten(t *testing.T) {
	h := newHarness(t)
	h.discovered(t)
	ctx := context.Background()
	shelf, err := eventrulecycle.Approve(ctx, h.deps, h.proposal(t, "lands on the shelf"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Proposals = failMarkRepo{h.repo}
	_, err = eventrulecycle.Apply(ctx, h.deps, []eventruleproposal.Proposal{shelf}, testNow)
	if err == nil || !strings.Contains(err.Error(), "rules file generated.yaml was written") || !strings.Contains(err.Error(), "run apply again to finish marking") {
		t.Fatalf("err %v", err)
	}
	if len(h.written) != 1 {
		t.Errorf("writes %d", len(h.written))
	}
}

// listHookRepo runs afterApproved once, right after the approved proposals
// are listed, to decide one of them before Apply re-reads it.
type listHookRepo struct {
	*sqliteeventruleproposal.Repository
	afterApproved func()
}

func (r *listHookRepo) ListByStatus(ctx context.Context, s eventruleproposal.Status) ([]eventruleproposal.Proposal, error) {
	ps, err := r.Repository.ListByStatus(ctx, s)
	if s == eventruleproposal.StatusApproved && r.afterApproved != nil {
		hook := r.afterApproved
		r.afterApproved = nil
		hook()
	}
	return ps, err
}

// rejectAfterListing makes the run's apply list phrase as approved and then
// rejects it in the store, as a panel decision racing the CLI.
func rejectAfterListing(t *testing.T, h *harness, phrase string) {
	t.Helper()
	repo := &listHookRepo{Repository: h.repo}
	repo.afterApproved = func() {
		p := h.byPhrase(t)[phrase]
		if _, err := eventrulecycle.Reject(context.Background(), eventrulecycle.Deps{Proposals: h.repo}, p, testNow); err != nil {
			t.Fatal(err)
		}
	}
	h.deps.Proposals = repo
}

func TestRun_ApplyWithNothingLeftPrintsNothing(t *testing.T) {
	h := newHarness(t)
	rejectAfterListing(t, h, "lands on the shelf")
	res, out := h.run(t, "a\nr\n")
	if res.Applied != 0 || res.GateRan || len(h.gateIn) != 0 {
		t.Fatalf("result %+v", res)
	}
	if strings.Contains(out, "Clustering gate") {
		t.Errorf("apply with nothing left printed a gate line:\n%s", out)
	}
}

func TestRun_BlockedListsTheProposalsGated(t *testing.T) {
	h := newHarness(t)
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "purity dropped"}
	rejectAfterListing(t, h, "inks a deal")
	_, out := h.run(t, "a\na\n")
	deal := h.byPhrase(t)["inks a deal"]
	if !strings.Contains(out, "Clustering gate blocked 1 approved rule(s): purity dropped") {
		t.Errorf("block count:\n%s", out)
	}
	if strings.Contains(out, "  - "+deal.ID) {
		t.Errorf("block lists a proposal rejected before the gate:\n%s", out)
	}
}
