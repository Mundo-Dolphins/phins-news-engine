// infra/eventrules/proposals.go
package eventrules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/eventruleproposalport"
)

var (
	// ErrNotTypeProposal reports a phrase proposal where a type proposal is
	// required; errors.Is matches it.
	ErrNotTypeProposal = errors.New("not a type proposal")
	// ErrAlreadyDecided reports a type proposal that is not in a state the
	// decision applies to; errors.Is matches it.
	ErrAlreadyDecided = errors.New("proposal already decided")
	// ErrBadMergeTarget reports a merge target that is the source itself, a
	// phrase proposal, a type proposal that is neither pending nor approved,
	// or a name that is not a known event type or action; errors.Is matches
	// it.
	ErrBadMergeTarget = errors.New("bad merge target")
	// ErrBadTypeApproval reports an approval whose window is not a positive
	// duration; errors.Is matches it.
	ErrBadTypeApproval = errors.New("bad type approval")
	// ErrTypeNotApproved reports a phrase added to a type proposal that is
	// not approved; errors.Is matches it.
	ErrTypeNotApproved = errors.New("type proposal not approved")
	// ErrEmptyPhrase reports a blank phrase; errors.Is matches it.
	ErrEmptyPhrase = errors.New("empty phrase")
)

// ManualPromptVersion is the prompt version of a phrase proposal the
// operator wrote (AddTypePhrase).
const ManualPromptVersion = "manual"

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

// TypeApproval holds the operator's choices when approving a type proposal:
// the match window (blank means 48h) and whether the type is referential.
// They are ignored when rejecting.
type TypeApproval struct {
	Window      string
	Referential bool
}

// DecideType approves or rejects the type proposal id at now and stores the
// result. A phrase proposal returns ErrNotTypeProposal, an already decided
// one ErrAlreadyDecided (carrying the domain error text), and an unknown id
// the repository's not-found error (errors.Is(err, sql.ErrNoRows)). An
// approval stores opts with the decision; a window that is not a positive
// duration returns ErrBadTypeApproval. A rejection also rejects the pending
// and approved phrase proposals targeting the type, unless its name is in
// known, the known event type and action names (KnownTypeNames): those
// phrases belong to that declared action. Nothing is applied here.
func DecideType(ctx context.Context, db *sql.DB, id string, approve bool, opts TypeApproval, known []string, now time.Time) (eventruleproposal.Proposal, error) {
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
	var decided eventruleproposal.Proposal
	if approve {
		decided, err = p.ApproveType(opts.Window, opts.Referential, now)
	} else {
		decided, err = p.Reject(now)
	}
	if err != nil {
		sentinel := ErrAlreadyDecided
		if approve && p.Status == eventruleproposal.StatusPending {
			sentinel = ErrBadTypeApproval // the kind was checked, so only the window can be wrong
		}
		return eventruleproposal.Proposal{}, &textError{msg: err.Error(), sentinel: sentinel}
	}
	// Guarded by the status read above: a decision landing in between
	// (the panel, another CLI) is reported, never overwritten.
	if err := repo.TransitionType(ctx, decided, p.Status); err != nil {
		if errors.Is(err, eventruleproposalport.ErrStatusChanged) {
			return eventruleproposal.Proposal{}, &textError{msg: fmt.Sprintf("proposal %s was decided elsewhere since it was read; reload it", p.ID), sentinel: ErrAlreadyDecided}
		}
		return eventruleproposal.Proposal{}, err
	}
	if !approve && !slices.Contains(known, decided.TypeName) {
		// A rejected type takes its phrases with it.
		if _, err := repo.RejectPhrasesFor(ctx, decided.TypeName, now); err != nil {
			return decided, err
		}
	}
	return decided, nil
}

// AddTypePhrase stores phrase, written by the operator, as a pending phrase
// proposal for the approved type proposal typeID at now: target kind action,
// target the type name, prompt version ManualPromptVersion and no evidence.
// It is then reviewed in the phrase queue like any other. A blank phrase
// returns ErrEmptyPhrase, a phrase proposal ErrNotTypeProposal, a type
// proposal that is not approved ErrTypeNotApproved, the same phrase already
// rejected ErrAlreadyDecided, and an unknown id the repository's not-found
// error. The same phrase still pending, approved or applied is returned as
// it is.
func AddTypePhrase(ctx context.Context, db *sql.DB, typeID, phrase string, now time.Time) (eventruleproposal.Proposal, error) {
	if strings.TrimSpace(phrase) == "" {
		return eventruleproposal.Proposal{}, &textError{msg: "the phrase is empty", sentinel: ErrEmptyPhrase}
	}
	repo := sqliteeventruleproposal.NewRepository(db)
	t, err := repo.Get(ctx, typeID)
	if err != nil {
		return eventruleproposal.Proposal{}, err
	}
	if t.Kind != eventruleproposal.KindType {
		return eventruleproposal.Proposal{}, &textError{
			msg:      fmt.Sprintf("proposal %s is a %s proposal, not a type proposal", t.ID, t.Kind),
			sentinel: ErrNotTypeProposal,
		}
	}
	if t.Status != eventruleproposal.StatusApproved {
		return eventruleproposal.Proposal{}, &textError{
			msg:      fmt.Sprintf("event type proposal %s (%s) is %s; approve it before adding phrases", t.ID, t.TypeName, t.Status),
			sentinel: ErrTypeNotApproved,
		}
	}
	p, err := repo.Upsert(ctx, eventruleproposal.Proposal{
		Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: t.TypeName,
		Phrase: phrase, Status: eventruleproposal.StatusPending, PromptVersion: ManualPromptVersion, CreatedAt: now.UTC(),
	})
	if err != nil {
		return eventruleproposal.Proposal{}, err
	}
	if p.Status == eventruleproposal.StatusRejected {
		return eventruleproposal.Proposal{}, &textError{
			msg:      fmt.Sprintf("phrase %q for %s was already rejected (%s)", p.Phrase, t.TypeName, p.ID),
			sentinel: ErrAlreadyDecided,
		}
	}
	return p, nil
}

// TypePhraseCounts counts the pending and approved action phrase proposals
// by target, the names of the types they would apply to.
func TypePhraseCounts(ctx context.Context, db *sql.DB) (map[string]int, error) {
	repo := sqliteeventruleproposal.NewRepository(db)
	counts := map[string]int{}
	for _, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved} {
		ps, err := repo.ListByStatus(ctx, st)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if p.Kind == eventruleproposal.KindPhrase && p.TargetKind == eventruleproposal.TargetAction {
				counts[p.Target]++
			}
		}
	}
	return counts, nil
}

// MergeResult is the outcome of MergeType: the rejected Source and either the
// Target proposal that gained its evidence (and, in Retargeted, the phrase
// proposals moved from the source's name to the target's) or, when merged
// into a known event type or action, the zero Target and that name in
// CoveredBy.
type MergeResult struct {
	Source, Target eventruleproposal.Proposal
	CoveredBy      string
	Retargeted     []eventruleproposal.Proposal
}

// MergeType folds the pending or approved type proposal id into into at now.
// An into starting with "evr_" is another pending or approved type proposal,
// which gains the source's evidence; anything else is slugged and must be in
// known, the known event type and action names (KnownTypeNames). Either way
// the source is rejected. The pending and approved phrase proposals of an
// approved source follow it: they are retargeted to the target proposal's
// name, or rejected when it is merged into a known name (either way kept as
// they are when the source's own name is in known: they belong to that
// action). A phrase source
// returns ErrNotTypeProposal, a
// decided one ErrAlreadyDecided, a refused target ErrBadMergeTarget, and an
// unknown id the repository's not-found error.
func MergeType(ctx context.Context, db *sql.DB, id, into string, known []string, now time.Time) (MergeResult, error) {
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
		if !slices.Contains(known, name) {
			return MergeResult{}, &textError{msg: fmt.Sprintf("%q is not a known event type or action", into), sentinel: ErrBadMergeTarget}
		}
		if err := repo.Transition(ctx, rejected, src.Status); err != nil {
			return MergeResult{}, decidedElsewhere(err)
		}
		if !slices.Contains(known, src.TypeName) {
			if _, err := repo.RejectPhrasesFor(ctx, src.TypeName, now); err != nil {
				return MergeResult{}, err
			}
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
	var retargeted []eventruleproposal.Proposal
	// A source named like a declared action does not own that action's
	// phrases: they stay where they are.
	if src.Status == eventruleproposal.StatusApproved && src.TypeName != dst.TypeName && !slices.Contains(known, src.TypeName) {
		if retargeted, err = repo.RetargetPhrases(ctx, src.TypeName, dst.TypeName); err != nil {
			return MergeResult{}, err
		}
	}
	merged, err := repo.MergeTypes(ctx, src.ID, src.Status, dst.ID, dst.Status, now)
	if err != nil {
		return MergeResult{}, decidedElsewhere(err)
	}
	return MergeResult{Source: rejected, Target: merged, Retargeted: retargeted}, nil
}

// NormalizeReport counts what NormalizeTypes did: the type proposals (not
// applied) Before and After, and the plan's counters.
type NormalizeReport struct {
	Before, After, Merged, Covered, Reopened, Emptied int
}

// NormalizeTypes folds every stored type proposal that is not applied onto
// its slug in one transaction (see eventruleproposal.PlanTypeNormalization),
// rejecting those covered by a name in known, the known event type and
// action names (KnownTypeNames). With reopenApproved every approved type
// proposal goes back to pending, not only merged ones. A second run with no
// decisions in between plans nothing.
func NormalizeTypes(ctx context.Context, db *sql.DB, reopenApproved bool, known []string, now time.Time) (NormalizeReport, error) {
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
	applied, err := repo.ListByStatus(ctx, eventruleproposal.StatusApplied)
	if err != nil {
		return NormalizeReport{}, err
	}
	appliedSlugs := map[string]bool{}
	for _, p := range applied {
		if p.Kind == eventruleproposal.KindType {
			appliedSlugs[eventruleproposal.Slug(p.TypeName)] = true
		}
	}
	covered := make(map[string]bool, len(known))
	for _, k := range known {
		covered[k] = true
	}
	plan := eventruleproposal.PlanTypeNormalization(ps, covered, appliedSlugs, reopenApproved, now)
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
