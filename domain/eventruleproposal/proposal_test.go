// domain/eventruleproposal/proposal_test.go
package eventruleproposal

import (
	"testing"
	"time"
)

func TestProposal_StateTransitions(t *testing.T) {
	now := time.Unix(100, 0)
	p := Proposal{ID: "p1", Status: StatusPending}

	a, err := p.Approve(now)
	if err != nil || a.Status != StatusApproved || !a.DecidedAt.Equal(now) {
		t.Fatalf("approve: %+v %v", a, err)
	}
	if p.Status != StatusPending {
		t.Fatal("original mutated")
	}
	ap, err := a.MarkApplied(now)
	if err != nil || ap.Status != StatusApplied {
		t.Fatalf("applied: %+v %v", ap, err)
	}
	r, err := p.Reject(now)
	if err != nil || r.Status != StatusRejected {
		t.Fatalf("reject: %+v %v", r, err)
	}
	if _, err := r.Approve(now); err == nil {
		t.Fatal("rejected->approved must error")
	}
	if _, err := p.MarkApplied(now); err == nil {
		t.Fatal("pending->applied must error")
	}
	ar, err := a.Reject(now)
	if err != nil || ar.Status != StatusRejected {
		t.Fatalf("approved->rejected: %+v %v", ar, err)
	}
	if _, err := ap.Reject(now); err == nil {
		t.Fatal("applied->rejected must error")
	}
	if _, err := r.Reject(now); err == nil {
		t.Fatal("rejected->rejected must error")
	}
	if _, err := ap.Approve(now); err == nil {
		t.Fatal("applied->approved must error")
	}
}

func TestApproveType(t *testing.T) {
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	pending := Proposal{ID: "evr_t", Kind: KindType, TypeName: "kicking-error", Status: StatusPending}

	got, err := pending.ApproveType("", false, now)
	if err != nil || got.Status != StatusApproved || got.TypeWindow != "48h" || got.TypeReferential || !got.DecidedAt.Equal(now) {
		t.Fatalf("default = %+v, %v; want approved with window 48h, not referential", got, err)
	}
	got, err = pending.ApproveType("72h", true, now)
	if err != nil || got.TypeWindow != "72h" || !got.TypeReferential {
		t.Fatalf("custom = %+v, %v; want window 72h, referential", got, err)
	}
	for _, bad := range []string{"0s", "abc", "-1h"} {
		if _, err := pending.ApproveType(bad, false, now); err == nil {
			t.Errorf("ApproveType(%q) accepted, want an error", bad)
		}
	}
	phrase := Proposal{ID: "evr_p", Kind: KindPhrase, Status: StatusPending}
	if _, err := phrase.ApproveType("", false, now); err == nil {
		t.Error("ApproveType on a phrase proposal accepted, want an error")
	}
	decided := Proposal{ID: "evr_d", Kind: KindType, Status: StatusRejected}
	if _, err := decided.ApproveType("", false, now); err == nil {
		t.Error("ApproveType on a rejected proposal accepted, want an error")
	}
}
