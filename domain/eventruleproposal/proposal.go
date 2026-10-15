// domain/eventruleproposal/proposal.go

// Package eventruleproposal models LLM-proposed event rules (new trigger
// phrases or new event types), their offline impact measurement and the
// deterministic checks that gate them. It is pure domain: no I/O.
package eventruleproposal

import (
	"fmt"
	"time"
)

// Kind distinguishes a trigger phrase proposal from a new event type.
type Kind string

const (
	KindPhrase Kind = "phrase"
	KindType   Kind = "type"
)

// Status is the lifecycle state of a proposal.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusApplied  Status = "applied"
)

// TargetKind selects which rule bucket a phrase proposal extends.
type TargetKind string

const (
	TargetAction TargetKind = "action"
	TargetSignal TargetKind = "signal"
)

// Proposal is a candidate rule awaiting review.
type Proposal struct {
	ID            string
	Kind          Kind
	TargetKind    TargetKind
	Target        string
	Phrase        string
	TypeName      string
	Description   string
	EvidenceIDs   []string
	Status        Status
	PromptVersion string
	CreatedAt     time.Time
	DecidedAt     time.Time
}

func (p Proposal) transition(from, to Status, now time.Time) (Proposal, error) {
	if p.Status != from {
		return p, fmt.Errorf("eventruleproposal: cannot move proposal %q from %q to %q", p.ID, p.Status, to)
	}
	p.Status = to
	p.DecidedAt = now
	return p, nil
}

// Approve moves a pending proposal to approved.
func (p Proposal) Approve(now time.Time) (Proposal, error) {
	return p.transition(StatusPending, StatusApproved, now)
}

// Reject moves a pending proposal, or an approved one not yet applied, to
// rejected.
func (p Proposal) Reject(now time.Time) (Proposal, error) {
	if p.Status == StatusApproved {
		return p.transition(StatusApproved, StatusRejected, now)
	}
	return p.transition(StatusPending, StatusRejected, now)
}

// MarkApplied moves an approved proposal to applied.
func (p Proposal) MarkApplied(now time.Time) (Proposal, error) {
	return p.transition(StatusApproved, StatusApplied, now)
}
