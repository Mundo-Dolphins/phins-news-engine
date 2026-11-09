package classifierdictionary

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/classifierproposalport"
)

// ErrNotPending is returned (via errors.Is) when a proposal being approved
// or rejected has already been decided.
var ErrNotPending = errors.New("proposal is not pending")

// reviewError carries a precise operator-facing message while matching a
// sentinel via errors.Is.
type reviewError struct {
	msg  string
	kind error
}

func (e *reviewError) Error() string { return e.msg }
func (e *reviewError) Unwrap() error { return e.kind }

func requirePending(p classifierproposal.Proposal) error {
	if p.Status != classifierproposal.StatusPending {
		return &reviewError{msg: fmt.Sprintf("proposal %s is already %s", p.ID, p.Status), kind: ErrNotPending}
	}
	return nil
}

// Approve approves a pending classifier proposal: it writes the change to
// the dictionary at dictionaryPath and then marks the row approved,
// returning the proposal. Repository errors (including a wrapped
// classifierproposal.ErrNotFound) pass through; a decided proposal yields
// ErrNotPending and nothing is written.
func Approve(ctx context.Context, repo classifierproposalport.ClassifierProposalRepository, id, dictionaryPath string, now time.Time) (classifierproposal.Proposal, error) {
	p, err := repo.Get(ctx, id)
	if err != nil {
		return classifierproposal.Proposal{}, err
	}
	if err := requirePending(p); err != nil {
		return classifierproposal.Proposal{}, err
	}
	if err := ApplyApproval(p, dictionaryPath, now); err != nil {
		return classifierproposal.Proposal{}, err
	}
	if err := repo.UpdateStatus(ctx, p.ID, classifierproposal.StatusApproved, now); err != nil {
		return classifierproposal.Proposal{}, err
	}
	return p, nil
}

// Reject marks a pending classifier proposal rejected and returns it.
// Unknown ids pass the repository's not-found error through; a decided
// proposal yields ErrNotPending.
func Reject(ctx context.Context, repo classifierproposalport.ClassifierProposalRepository, id string, now time.Time) (classifierproposal.Proposal, error) {
	p, err := repo.Get(ctx, id)
	if err != nil {
		return classifierproposal.Proposal{}, err
	}
	if err := requirePending(p); err != nil {
		return classifierproposal.Proposal{}, err
	}
	if err := repo.UpdateStatus(ctx, p.ID, classifierproposal.StatusRejected, now); err != nil {
		return classifierproposal.Proposal{}, err
	}
	return p, nil
}
