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
