// app/eventrulecycle/steps.go
package eventrulecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// The non-interactive steps of the cycle. Run composes them with the
// operator prompts; the admin panel calls them one decision at a time.

var (
	// ErrPhraseUnchanged reports an edit whose normalised phrase is empty or
	// equal to the original; errors.Is matches it.
	ErrPhraseUnchanged = errors.New("phrase unchanged")
	// ErrNoCitedEvidence reports an edited phrase that no article cited by
	// the original proposal contains; errors.Is matches it.
	ErrNoCitedEvidence = errors.New("no cited article contains the edited phrase")
	// ErrEditFailsChecks reports an edited phrase that fails the checks; the
	// error text carries the reasons and errors.Is matches it.
	ErrEditFailsChecks = errors.New("edited phrase fails the checks")
	// ErrAlreadyDecided reports a proposal that is no longer in a status the
	// decision applies to (decided elsewhere, or an edit onto an already
	// decided phrase); errors.Is matches it.
	ErrAlreadyDecided = errors.New("proposal already decided")
)

// Context is what the review of one cycle is judged against: the base rules
// (hand-written plus current generated), the generic sample, the typed
// control sample and the lowercased title + summary of each sampled article.
type Context struct {
	Base    eventsignals.Rules
	Sample  []eventruleproposal.Sample
	Control []eventruleproposal.Sample
	Text    map[string]string // article ID -> lowercased title + summary
}

// Prepare draws the sample, then loads the base rules and the typed control
// sample. An empty sample returns a Context with an empty sample and no
// error, without loading the rest: nothing is discovered or reviewed then.
func Prepare(ctx context.Context, d Deps) (Context, error) {
	sample, err := d.Sampler(ctx)
	if err != nil {
		return Context{}, fmt.Errorf("eventrulecycle: sample: %w", err)
	}
	if len(sample) == 0 {
		return Context{Sample: sample, Text: map[string]string{}}, nil
	}
	base, err := d.BaseRules()
	if err != nil {
		return Context{}, fmt.Errorf("eventrulecycle: base rules: %w", err)
	}
	var control []eventruleproposal.Sample
	if d.Control != nil {
		if control, err = d.Control(ctx); err != nil {
			return Context{}, fmt.Errorf("eventrulecycle: typed control sample: %w", err)
		}
	}
	return Context{Base: base, Sample: sample, Control: control, Text: sampleText(sample)}, nil
}

// DiscoverResult summarises one discovery pass. Unavailable is true when
// every LLM call failed; nothing is stored then.
type DiscoverResult struct {
	Sampled, Calls, Failed, Proposed int
	FirstErr                         error
	Unavailable                      bool
}

// Discover asks the LLM for topics and phrases over c's sample in batches of
// batchSize (20 when not positive) and stores the valid ones as pending
// proposals. Failed calls are counted, never returned as an error.
func Discover(ctx context.Context, d Deps, c Context, batchSize int) (DiscoverResult, error) {
	r, _, err := discoverAndStore(ctx, d, c, batchSize)
	return r, err
}

// discoverAndStore is Discover that also returns the IDs of the pending type
// proposals stored or updated, which Run lists at the end.
func discoverAndStore(ctx context.Context, d Deps, c Context, batchSize int) (DiscoverResult, map[string]bool, error) {
	topics, phrases, failed, calls, firstErr := discover(ctx, d.LLM, c.Sample, batchSize)
	r := DiscoverResult{Sampled: len(c.Sample), Calls: calls, Failed: failed, FirstErr: firstErr}
	if calls > 0 && failed == calls {
		r.Unavailable = true
		return r, nil, nil
	}
	n, typesNow, err := persistProposals(ctx, d.Proposals, c.Base, c.Text, topics, phrases, nowOf(d)().UTC())
	r.Proposed = n
	return r, typesNow, err
}

// Assessment is a phrase proposal measured against a Context. NoEvidence is
// true when the verdict fails only because the sample has no match, which is
// no reason to reject it.
type Assessment struct {
	Proposal   eventruleproposal.Proposal
	Impact     eventruleproposal.ImpactResult
	Verdict    eventruleproposal.Verdict
	NoEvidence bool
}

// Assess normalises p's phrase and measures it over c's generic plus typed
// control sample. It writes nothing.
func Assess(d Deps, c Context, p eventruleproposal.Proposal, opts eventruleproposal.CheckOptions) Assessment {
	return assessAt(c, p, opts, nowOf(d)())
}

func assessAt(c Context, p eventruleproposal.Proposal, opts eventruleproposal.CheckOptions, now time.Time) Assessment {
	p.Phrase = normalizePhrase(p.Phrase)
	impact := impactOf(c, p, now)
	verdict := eventruleproposal.Check(p, impact, len(c.Sample), opts)
	return Assessment{Proposal: p, Impact: impact, Verdict: verdict, NoEvidence: onlyNoMatches(verdict)}
}

// impactOf measures p over c's generic sample plus the typed control
// sample, so Check can count already-typed articles a phrase would retype.
func impactOf(c Context, p eventruleproposal.Proposal, now time.Time) eventruleproposal.ImpactResult {
	all := c.Sample
	if len(c.Control) > 0 {
		all = append(append([]eventruleproposal.Sample(nil), c.Sample...), c.Control...)
	}
	return eventruleproposal.Impact(c.Base, p, all, now.UTC())
}

// Pending assesses every pending phrase proposal, sorted by target kind,
// target and phrase. Nothing is decided: failing ones are only flagged.
func Pending(ctx context.Context, d Deps, c Context, opts eventruleproposal.CheckOptions) ([]Assessment, error) {
	pending, err := phrasesByStatus(ctx, d.Proposals, eventruleproposal.StatusPending)
	if err != nil {
		return nil, err
	}
	now := nowOf(d)()
	out := make([]Assessment, 0, len(pending))
	for _, p := range pending {
		out = append(out, assessAt(c, p, opts, now))
	}
	return out, nil
}

// ApprovedNotApplied returns the approved phrase proposals that are not yet
// in the generated rules file, sorted by target kind, target and phrase.
func ApprovedNotApplied(ctx context.Context, d Deps) ([]eventruleproposal.Proposal, error) {
	return phrasesByStatus(ctx, d.Proposals, eventruleproposal.StatusApproved)
}

// Approve approves the stored proposal p.ID at now. A proposal no longer
// pending returns ErrAlreadyDecided.
func Approve(ctx context.Context, d Deps, p eventruleproposal.Proposal, now time.Time) (eventruleproposal.Proposal, error) {
	return decide(ctx, d, p.ID, "approve", eventruleproposal.Proposal.Approve, now)
}

// Reject rejects the stored proposal p.ID (pending, or approved and not yet
// applied) at now. Any other status returns ErrAlreadyDecided.
func Reject(ctx context.Context, d Deps, p eventruleproposal.Proposal, now time.Time) (eventruleproposal.Proposal, error) {
	return decide(ctx, d, p.ID, "reject", eventruleproposal.Proposal.Reject, now)
}

// decide re-reads proposal id, so a stale copy never overwrites a decision
// taken elsewhere, applies transition and saves the result; verb names the
// decision in errors.
func decide(ctx context.Context, d Deps, id, verb string, transition func(eventruleproposal.Proposal, time.Time) (eventruleproposal.Proposal, error), now time.Time) (eventruleproposal.Proposal, error) {
	cur, err := d.Proposals.Get(ctx, id)
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("eventrulecycle: reload %s: %w", id, err)
	}
	decided, err := transition(cur, now.UTC())
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("eventrulecycle: %s %s: %w: %w", verb, id, ErrAlreadyDecided, err)
	}
	if err := d.Proposals.Save(ctx, decided); err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("eventrulecycle: %s %s: %w", verb, id, err)
	}
	return decided, nil
}

// Edit replaces p's phrase with phrase (normalised) as the CLI `edit` does:
// the phrase must differ, its evidence is the articles cited by p that
// contain it, and it must pass the checks. With save false it returns the
// assessment of the edited phrase and writes nothing; with save true it
// stores the edited rule as its own proposal, approves it and rejects p as
// superseded, returning the approved proposal.
//
// Errors: ErrPhraseUnchanged, ErrNoCitedEvidence (the Assessment carries the
// edited phrase), ErrEditFailsChecks (the Assessment carries the verdict),
// and ErrAlreadyDecided when the edited rule is already stored in another
// status (the Assessment carries that stored proposal) or p was decided
// elsewhere.
func Edit(ctx context.Context, d Deps, c Context, p eventruleproposal.Proposal, phrase string, opts eventruleproposal.CheckOptions, now time.Time, save bool) (Assessment, error) {
	edited := p
	edited.ID = ""
	edited.Phrase = normalizePhrase(phrase)
	edited.CreatedAt = now.UTC()
	edited.DecidedAt = time.Time{}
	if edited.Phrase == "" || edited.Phrase == normalizePhrase(p.Phrase) {
		return Assessment{Proposal: edited}, ErrPhraseUnchanged
	}
	edited.EvidenceIDs = nil
	for _, id := range p.EvidenceIDs {
		if strings.Contains(c.Text[id], edited.Phrase) {
			edited.EvidenceIDs = append(edited.EvidenceIDs, id)
		}
	}
	if len(edited.EvidenceIDs) == 0 {
		return Assessment{Proposal: edited}, fmt.Errorf("%w: %q", ErrNoCitedEvidence, edited.Phrase)
	}
	a := assessAt(c, edited, opts, now)
	if !a.Verdict.OK {
		return a, fmt.Errorf("%w: %q: %s", ErrEditFailsChecks, edited.Phrase, strings.Join(a.Verdict.Reasons, "; "))
	}
	if !save {
		return a, nil
	}
	stored, err := d.Proposals.Upsert(ctx, edited)
	if err != nil {
		return Assessment{}, fmt.Errorf("eventrulecycle: store edited phrase %q: %w", edited.Phrase, err)
	}
	if stored.Status != eventruleproposal.StatusPending {
		a.Proposal = stored
		return a, fmt.Errorf("%w: phrase %q was already %s", ErrAlreadyDecided, stored.Phrase, stored.Status)
	}
	approved, err := Approve(ctx, d, stored, now)
	if err != nil {
		return Assessment{}, err
	}
	if _, err := decide(ctx, d, p.ID, "supersede", eventruleproposal.Proposal.Reject, now); err != nil {
		return Assessment{}, err
	}
	a.Proposal = approved
	return a, nil
}

// ApplyResult is the outcome of Apply. Candidate is the full rule set the
// gate scored (nil when the gate did not score one); Applied the proposals
// marked applied; Blocked and Reason report a gate refusal, after which the
// proposals stay approved and no file is written; Nothing means there was
// nothing to apply and the gate did not run.
type ApplyResult struct {
	Applied   []eventruleproposal.Proposal
	Candidate []AppliedRule
	Gate      GateResult
	Blocked   bool
	Reason    string
	Nothing   bool
}

// Apply gates the rules already in the generated file plus toApply (sorted
// and deduplicated) against the clustering benchmark. When the gate passes it
// writes the generated file and marks toApply applied at now.
func Apply(ctx context.Context, d Deps, toApply []eventruleproposal.Proposal, now time.Time) (ApplyResult, error) {
	var r ApplyResult
	if len(toApply) == 0 {
		r.Nothing = true
		return r, nil
	}
	toApply = append([]eventruleproposal.Proposal(nil), toApply...)
	sortProposals(toApply)
	if d.Gate == nil || d.ReadApplied == nil || d.WriteApplied == nil {
		return r, errors.New("eventrulecycle: Gate, ReadApplied and WriteApplied are required to apply approved rules")
	}
	existing, err := d.ReadApplied(d.GeneratedPath)
	if err != nil {
		return r, fmt.Errorf("eventrulecycle: read generated rules: %w", err)
	}
	candidate := make([]AppliedRule, 0, len(existing)+len(toApply))
	candidate = append(candidate, existing...)
	seen := make(map[string]bool, len(candidate))
	for _, rule := range candidate {
		seen[ruleKey(rule)] = true
	}
	for _, p := range toApply {
		rule := AppliedRule{TargetKind: string(p.TargetKind), Target: p.Target, Phrase: normalizePhrase(p.Phrase), ProposalID: p.ID, PromptVersion: p.PromptVersion}
		if seen[ruleKey(rule)] {
			continue
		}
		seen[ruleKey(rule)] = true
		candidate = append(candidate, rule)
	}

	gate, err := d.Gate(ctx, candidate)
	if err != nil {
		return r, fmt.Errorf("eventrulecycle: clustering gate: %w", err)
	}
	r.Candidate, r.Gate = candidate, gate
	if !gate.OK {
		r.Blocked, r.Reason = true, gate.Reason
		return r, nil
	}
	if err := d.WriteApplied(d.GeneratedPath, candidate); err != nil {
		return r, fmt.Errorf("eventrulecycle: write generated rules: %w", err)
	}
	for _, p := range toApply {
		applied, err := p.MarkApplied(now.UTC())
		if err != nil {
			return r, fmt.Errorf("eventrulecycle: mark applied %s: %w", p.ID, err)
		}
		if err := d.Proposals.Save(ctx, applied); err != nil {
			return r, fmt.Errorf("eventrulecycle: mark applied %s: %w", p.ID, err)
		}
		r.Applied = append(r.Applied, applied)
	}
	return r, nil
}

func nowOf(d Deps) func() time.Time {
	if d.Now == nil {
		return time.Now
	}
	return d.Now
}
