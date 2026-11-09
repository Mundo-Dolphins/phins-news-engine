package entitydiscovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
)

var (
	// ErrNotPending is returned (via errors.Is) when a proposal being
	// approved or rejected has already been decided.
	ErrNotPending = errors.New("proposal is not pending")
	// ErrNeedsResolution is returned (via errors.Is) when a possible
	// duplicate is approved without choosing a merge target or as-new.
	ErrNeedsResolution = errors.New("possible duplicate needs a resolution")
	// ErrConflictingResolution is returned (via errors.Is) when both a
	// merge target and as-new are requested.
	ErrConflictingResolution = errors.New("conflicting duplicate resolutions")
)

// reviewError carries a precise operator-facing message while matching a
// sentinel via errors.Is.
type reviewError struct {
	msg  string
	kind error
}

func (e *reviewError) Error() string { return e.msg }
func (e *reviewError) Unwrap() error { return e.kind }

// Resolution says how to approve a possible-duplicate proposal: MergeInto
// treats it as an alias of that catalog entity id, AsNew as a brand new
// entity. At most one may be set; it is ignored for other proposal kinds.
type Resolution struct {
	MergeInto string
	AsNew     bool
}

// Approve approves a pending proposal: it applies it to the catalog files
// and then marks the row approved. It returns the resolved proposal (for a
// possible duplicate, the alias or new-entity form that was applied).
// Errors from the repository (including a wrapped
// entityproposal.ErrNotFound) pass through; a decided proposal yields
// ErrNotPending, an unresolved duplicate ErrNeedsResolution and both
// resolutions ErrConflictingResolution. No file or row changes on those.
func Approve(ctx context.Context, repo entityproposalport.EntityProposalRepository, id string, r Resolution, paths CatalogPaths, now time.Time) (entityproposal.Proposal, error) {
	p, err := repo.Get(ctx, id)
	if err != nil {
		return entityproposal.Proposal{}, err
	}
	if p.Status != entityproposal.StatusPending {
		return entityproposal.Proposal{}, &reviewError{msg: fmt.Sprintf("proposal %s is already %s", p.ID, p.Status), kind: ErrNotPending}
	}

	resolved := p
	if p.Kind == entityproposal.KindPossibleDuplicate {
		switch {
		case r.MergeInto != "" && r.AsNew:
			return entityproposal.Proposal{}, &reviewError{msg: "pass only one of --merge-into or --as-new", kind: ErrConflictingResolution}
		case r.MergeInto != "":
			resolved.Kind = entityproposal.KindNewAlias
			resolved.TargetEntityID = r.MergeInto
		case r.AsNew:
			resolved.Kind = entityproposal.KindNewEntity
		default:
			return entityproposal.Proposal{}, &reviewError{msg: fmt.Sprintf("proposal %s is a possible duplicate — pass --merge-into <entity-id> or --as-new", p.ID), kind: ErrNeedsResolution}
		}
	}

	if err := ApplyApproval(resolved, paths, now); err != nil {
		return entityproposal.Proposal{}, err
	}
	if err := repo.UpdateStatus(ctx, p.ID, entityproposal.StatusApproved, now); err != nil {
		return entityproposal.Proposal{}, err
	}
	return resolved, nil
}

// Reject marks a pending proposal rejected without touching any catalog
// file and returns it. Unknown ids pass the repository's not-found error
// through; a decided proposal yields ErrNotPending.
func Reject(ctx context.Context, repo entityproposalport.EntityProposalRepository, id string, now time.Time) (entityproposal.Proposal, error) {
	p, err := repo.Get(ctx, id)
	if err != nil {
		return entityproposal.Proposal{}, err
	}
	if p.Status != entityproposal.StatusPending {
		return entityproposal.Proposal{}, &reviewError{msg: fmt.Sprintf("proposal %s is already %s", p.ID, p.Status), kind: ErrNotPending}
	}
	if err := repo.UpdateStatus(ctx, p.ID, entityproposal.StatusRejected, now); err != nil {
		return entityproposal.Proposal{}, err
	}
	return p, nil
}
