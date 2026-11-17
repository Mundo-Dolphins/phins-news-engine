// infra/eventrules/proposals.go
package eventrules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/eventruleproposalport"
)

// maxBriefExamples bounds the evidence titles quoted in a brief.
const maxBriefExamples = 5

var (
	// ErrNotTypeProposal reports a phrase proposal where a type proposal is
	// required; errors.Is matches it.
	ErrNotTypeProposal = errors.New("not a type proposal")
	// ErrNoBrief reports a type proposal whose status is neither pending nor
	// approved; errors.Is matches it.
	ErrNoBrief = errors.New("proposal has no brief")
	// ErrAlreadyDecided reports a type proposal that is not in a state the
	// decision applies to; errors.Is matches it.
	ErrAlreadyDecided = errors.New("proposal already decided")
	// ErrBadMergeTarget reports a merge target that is the source itself, a
	// phrase proposal, a type proposal that is neither pending nor approved,
	// or a name that is not a known event type or action; errors.Is matches
	// it.
	ErrBadMergeTarget = errors.New("bad merge target")
)

// textError carries a fixed message and matches a sentinel through errors.Is.
type textError struct {
	msg      string
	sentinel error
}

func (e *textError) Error() string { return e.msg }
func (e *textError) Unwrap() error { return e.sentinel }

// ListTypes returns the pending, then the approved, event type proposals.
func ListTypes(ctx context.Context, db *sql.DB) ([]eventruleproposal.Proposal, error) {
	repo := sqliteeventruleproposal.NewRepository(db)
	var out []eventruleproposal.Proposal
	for _, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved} {
		ps, err := repo.ListByStatus(ctx, st)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if p.Kind == eventruleproposal.KindType {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// DecideType approves or rejects the type proposal id at now and stores the
// result. A phrase proposal returns ErrNotTypeProposal, an already decided
// one ErrAlreadyDecided (carrying the domain error text), and an unknown id
// the repository's not-found error (errors.Is(err, sql.ErrNoRows)). Nothing is applied: a type needs a code
// change, see Brief.
func DecideType(ctx context.Context, db *sql.DB, id string, approve bool, now time.Time) (eventruleproposal.Proposal, error) {
	repo := sqliteeventruleproposal.NewRepository(db)
	p, err := repo.Get(ctx, id)
	if err != nil {
		return eventruleproposal.Proposal{}, err
	}
	if p.Kind != eventruleproposal.KindType {
		return eventruleproposal.Proposal{}, &textError{
			msg:      fmt.Sprintf("proposal %s is a %s proposal, not a type proposal; phrases are decided in `events review`", p.ID, p.Kind),
			sentinel: ErrNotTypeProposal,
		}
	}
	decide := eventruleproposal.Proposal.Reject
	if approve {
		decide = eventruleproposal.Proposal.Approve
	}
	decided, err := decide(p, now)
	if err != nil {
		return eventruleproposal.Proposal{}, &textError{msg: err.Error(), sentinel: ErrAlreadyDecided}
	}
	// Guarded by the status read above: a decision landing in between
	// (the panel, another CLI) is reported, never overwritten.
	if err := repo.Transition(ctx, decided, p.Status); err != nil {
		if errors.Is(err, eventruleproposalport.ErrStatusChanged) {
			return eventruleproposal.Proposal{}, &textError{msg: fmt.Sprintf("proposal %s was decided elsewhere since it was read; reload it", p.ID), sentinel: ErrAlreadyDecided}
		}
		return eventruleproposal.Proposal{}, err
	}
	return decided, nil
}

// Brief returns the implementation brief of the pending or approved type
// proposal id. A phrase proposal returns ErrNotTypeProposal, any other
// status ErrNoBrief, an unknown id the repository's not-found error.
func Brief(ctx context.Context, db *sql.DB, id string) (string, error) {
	p, err := sqliteeventruleproposal.NewRepository(db).Get(ctx, id)
	if err != nil {
		return "", err
	}
	if p.Kind != eventruleproposal.KindType {
		return "", &textError{
			msg:      fmt.Sprintf("proposal %s is a %s proposal, not a type proposal", p.ID, p.Kind),
			sentinel: ErrNotTypeProposal,
		}
	}
	if p.Status != eventruleproposal.StatusPending && p.Status != eventruleproposal.StatusApproved {
		return "", &textError{
			msg:      fmt.Sprintf("proposal %s is %s; only pending or approved type proposals have a brief", p.ID, p.Status),
			sentinel: ErrNoBrief,
		}
	}
	articles := sqliteadapter.NewArticleRepository(db)
	var examples []string
	for _, eid := range p.EvidenceIDs {
		if len(examples) >= maxBriefExamples {
			break
		}
		a, err := articles.FindByID(ctx, eid)
		if err != nil || a.Title == "" {
			continue // evidence may have been pruned
		}
		examples = append(examples, a.Title)
	}
	return eventrulecycle.Brief(p, examples), nil
}

// MergeResult is the outcome of MergeType: the rejected Source and either the
// Target proposal that gained its evidence or, when merged into a known
// event type or action, the zero Target and that name in CoveredBy.
type MergeResult struct {
	Source, Target eventruleproposal.Proposal
	CoveredBy      string
}

// MergeType folds the pending or approved type proposal id into into at now.
// An into starting with "evr_" is another pending or approved type proposal,
// which gains the source's evidence; anything else is slugged and must be a
// known event type or action. Either way the source is rejected. A phrase
// source returns ErrNotTypeProposal, a decided one ErrAlreadyDecided, a
// refused target ErrBadMergeTarget, and an unknown id the repository's
// not-found error.
func MergeType(ctx context.Context, db *sql.DB, id, into string, now time.Time) (MergeResult, error) {
	if into == id {
		return MergeResult{}, &textError{msg: fmt.Sprintf("proposal %s cannot be merged into itself", id), sentinel: ErrBadMergeTarget}
	}
	repo := sqliteeventruleproposal.NewRepository(db)
	src, err := repo.Get(ctx, id)
	if err != nil {
		return MergeResult{}, err
	}
	if src.Kind != eventruleproposal.KindType {
		return MergeResult{}, &textError{
			msg:      fmt.Sprintf("proposal %s is a %s proposal, not a type proposal", src.ID, src.Kind),
			sentinel: ErrNotTypeProposal,
		}
	}
	rejected, err := src.Reject(now)
	if err != nil {
		return MergeResult{}, &textError{msg: err.Error(), sentinel: ErrAlreadyDecided}
	}
	decidedElsewhere := func(err error) error {
		if errors.Is(err, eventruleproposalport.ErrStatusChanged) {
			return &textError{msg: fmt.Sprintf("proposal %s or its merge target was decided elsewhere since it was read; reload it", src.ID), sentinel: ErrAlreadyDecided}
		}
		return err
	}

	if !strings.HasPrefix(into, "evr_") {
		name := eventruleproposal.Slug(into)
		if !isKnownTypeName(name) {
			return MergeResult{}, &textError{msg: fmt.Sprintf("%q is not a known event type or action", into), sentinel: ErrBadMergeTarget}
		}
		if err := repo.Transition(ctx, rejected, src.Status); err != nil {
			return MergeResult{}, decidedElsewhere(err)
		}
		return MergeResult{Source: rejected, CoveredBy: name}, nil
	}

	dst, err := repo.Get(ctx, into)
	if err != nil {
		return MergeResult{}, err
	}
	if dst.Kind != eventruleproposal.KindType {
		return MergeResult{}, &textError{msg: fmt.Sprintf("merge target %s is a %s proposal, not a type proposal", dst.ID, dst.Kind), sentinel: ErrBadMergeTarget}
	}
	if dst.Status != eventruleproposal.StatusPending && dst.Status != eventruleproposal.StatusApproved {
		return MergeResult{}, &textError{msg: fmt.Sprintf("merge target %s is %s; only pending or approved type proposals take a merge", dst.ID, dst.Status), sentinel: ErrBadMergeTarget}
	}
	merged, err := repo.MergeTypes(ctx, src.ID, src.Status, dst.ID, dst.Status, now)
	if err != nil {
		return MergeResult{}, decidedElsewhere(err)
	}
	return MergeResult{Source: rejected, Target: merged}, nil
}

// isKnownTypeName reports whether name is a known event type or action.
func isKnownTypeName(name string) bool {
	for _, k := range eventrulecycle.KnownTypeNames() {
		if k == name {
			return true
		}
	}
	return false
}

// NormalizeReport counts what NormalizeTypes did: the type proposals (not
// applied) Before and After, and the plan's counters.
type NormalizeReport struct {
	Before, After, Merged, Covered, Reopened, Emptied int
}

// NormalizeTypes folds every stored type proposal that is not applied onto
// its slug in one transaction (see eventruleproposal.PlanTypeNormalization),
// rejecting those covered by a known event type or action. With
// reopenApproved every approved type proposal goes back to pending, not only
// merged ones. A second run with no decisions in between plans nothing.
func NormalizeTypes(ctx context.Context, db *sql.DB, reopenApproved bool, now time.Time) (NormalizeReport, error) {
	repo := sqliteeventruleproposal.NewRepository(db)
	var ps []eventruleproposal.Proposal
	for _, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved, eventruleproposal.StatusRejected} {
		list, err := repo.ListByStatus(ctx, st)
		if err != nil {
			return NormalizeReport{}, err
		}
		for _, p := range list {
			if p.Kind == eventruleproposal.KindType {
				ps = append(ps, p)
			}
		}
	}
	known := map[string]bool{}
	for _, k := range eventrulecycle.KnownTypeNames() {
		known[k] = true
	}
	plan := eventruleproposal.PlanTypeNormalization(ps, known, reopenApproved, now)
	if len(plan.Delete) > 0 || len(plan.Insert) > 0 {
		if err := repo.ReplaceTypes(ctx, plan.Delete, plan.Insert); err != nil {
			return NormalizeReport{}, err
		}
	}
	return NormalizeReport{
		Before: len(ps), After: len(ps) - len(plan.Delete) + len(plan.Insert),
		Merged: plan.Merged, Covered: plan.Covered, Reopened: plan.Reopened, Emptied: plan.Emptied,
	}, nil
}
