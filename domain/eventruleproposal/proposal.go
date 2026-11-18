// domain/eventruleproposal/proposal.go

// Package eventruleproposal models LLM-proposed event rules (new trigger
// phrases or new event types), their offline impact measurement and the
// deterministic checks that gate them. It is pure domain: no I/O.
package eventruleproposal

import (
	"fmt"
	"strings"
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
	// TypeWindow and TypeReferential are the approval choices of a type
	// proposal: the match window (a Go duration string) and whether the type
	// is referential. They become the type's entry in the generated rules
	// file; a blank TypeWindow reads as DefaultTypeWindow.
	TypeWindow      string
	TypeReferential bool
}

// DefaultTypeWindow is the match window of an approved type whose operator
// chose none.
const DefaultTypeWindow = "48h"

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

// ApproveType approves a pending type proposal with the operator's choices:
// the match window (blank means DefaultTypeWindow, otherwise it must parse to
// a positive duration) and whether the type is referential. A phrase proposal
// is refused. Otherwise it behaves like Approve.
func (p Proposal) ApproveType(window string, referential bool, now time.Time) (Proposal, error) {
	if p.Kind != KindType {
		return p, fmt.Errorf("eventruleproposal: proposal %q is a %s proposal, not a type proposal", p.ID, p.Kind)
	}
	window = strings.TrimSpace(window)
	if window == "" {
		window = DefaultTypeWindow
	}
	if d, err := time.ParseDuration(window); err != nil || d <= 0 {
		return p, fmt.Errorf("eventruleproposal: window %q is not a positive duration", window)
	}
	approved, err := p.Approve(now)
	if err != nil {
		return p, err
	}
	approved.TypeWindow, approved.TypeReferential = window, referential
	return approved, nil
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
