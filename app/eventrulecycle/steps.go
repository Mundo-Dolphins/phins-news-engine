// app/eventrulecycle/steps.go
package eventrulecycle

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/eventruleproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
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
	// ErrApplyStale reports an Apply whose proposals were decided elsewhere
	// while the gate ran; nothing was written. errors.Is matches it.
	ErrApplyStale = errors.New("proposals changed during the gate; run apply again")
)

// Context is what the review of one cycle is judged against: the base rules
// (hand-written plus current generated, extended with the approved type
// proposals not yet applied), the generic sample, the typed control sample
// and the lowercased title + summary of each sampled article. TypeTargets
// holds the names of the approved type proposals and of the generated
// file's types: the actions a framing-only retype is allowed for.
type Context struct {
	Base        eventsignals.Rules
	Sample      []eventruleproposal.Sample
	Control     []eventruleproposal.Sample
	Text        map[string]string // article ID -> lowercased title + summary
	TypeTargets map[eventsignals.Action]bool
}

// Prepare draws the sample, then loads the base rules (extended with the
// approved type proposals, see withApprovedTypes) and the typed control
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
	base, targets, err := withApprovedTypes(ctx, d, base)
	if err != nil {
		return Context{}, err
	}
	var control []eventruleproposal.Sample
	if d.Control != nil {
		if control, err = d.Control(ctx); err != nil {
			return Context{}, fmt.Errorf("eventrulecycle: typed control sample: %w", err)
		}
	}
	return Context{Base: base, Sample: sample, Control: control, Text: sampleText(sample), TypeTargets: targets}, nil
}

// withApprovedTypes returns a copy of base with each approved type proposal
// whose name base does not declare appended as an event type, an action of
// the same name after the existing ones and its window, so impact, the
// target check and the LLM's lists see it. It also returns the names of
// those proposals plus the generated file's types (read when ReadApplied is
// set).
func withApprovedTypes(ctx context.Context, d Deps, base eventsignals.Rules) (eventsignals.Rules, map[eventsignals.Action]bool, error) {
	targets := map[eventsignals.Action]bool{}
	if d.ReadApplied != nil {
		set, err := d.ReadApplied(d.GeneratedPath)
		if err != nil {
			return base, nil, fmt.Errorf("eventrulecycle: read generated rules: %w", err)
		}
		for _, t := range set.Types {
			targets[eventsignals.Action(t.Name)] = true
		}
	}
	types, err := approvedTypes(ctx, d.Proposals)
	if err != nil {
		return base, nil, err
	}
	declared := declaredNames(base.Taxonomy)
	out := base
	extended := false
	for _, p := range types {
		name := p.TypeName
		if declared[name] {
			continue
		}
		targets[eventsignals.Action(name)] = true
		if !extended {
			extended = true
			out.Taxonomy.EventTypes = slices.Clone(base.Taxonomy.EventTypes)
			out.Taxonomy.Actions = slices.Clone(base.Taxonomy.Actions)
			out.EventWindows = maps.Clone(base.EventWindows)
			if out.EventWindows == nil {
				out.EventWindows = map[eventsignals.EventType]time.Duration{}
			}
		}
		et := eventsignals.EventType(name)
		def := eventsignals.EventTypeDef{Name: et, Description: p.Description}
		if p.TypeReferential {
			def.Flags = []eventsignals.TypeFlag{eventsignals.FlagReferential}
		}
		out.Taxonomy.EventTypes = append(out.Taxonomy.EventTypes, def)
		out.Taxonomy.Actions = append(out.Taxonomy.Actions, eventsignals.ActionDef{Name: eventsignals.Action(name), EventType: et})
		out.EventWindows[et] = typeWindow(p)
	}
	return out, targets, nil
}

// typeWindow parses p's approval window; a blank or invalid one (the
// approval checks it) is DefaultTypeWindow.
func typeWindow(p eventruleproposal.Proposal) time.Duration {
	if w, err := time.ParseDuration(p.TypeWindow); err == nil && w > 0 {
		return w
	}
	w, _ := time.ParseDuration(eventruleproposal.DefaultTypeWindow)
	return w
}

// declaredNames returns t's event type and action names.
func declaredNames(t eventsignals.Taxonomy) map[string]bool {
	out := map[string]bool{}
	for _, n := range KnownTypeNames(t) {
		out[n] = true
	}
	return out
}

// approvedTypes returns the approved type proposals sorted by name.
func approvedTypes(ctx context.Context, repo eventruleproposalport.Repository) ([]eventruleproposal.Proposal, error) {
	all, err := repo.ListByStatus(ctx, eventruleproposal.StatusApproved)
	if err != nil {
		return nil, fmt.Errorf("eventrulecycle: list approved proposals: %w", err)
	}
	var out []eventruleproposal.Proposal
	for _, p := range all {
		if p.Kind == eventruleproposal.KindType {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TypeName < out[j].TypeName })
	return out, nil
}

// DiscoverResult summarises one discovery pass. Unavailable is true when
// every LLM call failed; nothing is stored then.
type DiscoverResult struct {
	Sampled, Calls, Failed, Proposed, Dropped int
	FirstErr                                  error
	Unavailable                               bool
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
	known, err := knownKinds(ctx, d.Proposals, c.Base.Taxonomy)
	if err != nil {
		return DiscoverResult{}, nil, err
	}
	topics, phrases, failed, calls, firstErr := discover(ctx, d.LLM, c.Sample, batchSize, known, c.Base.Taxonomy)
	r := DiscoverResult{Sampled: len(c.Sample), Calls: calls, Failed: failed, FirstErr: firstErr}
	if calls > 0 && failed == calls {
		r.Unavailable = true
		return r, nil, nil
	}
	n, typesNow, dropped, err := persistProposals(ctx, d.Proposals, c.Base, c.Text, topics, phrases, nowOf(d)().UTC())
	r.Proposed, r.Dropped = n, dropped
	return r, typesNow, err
}

// knownKinds is KnownTypeNames(t) plus the slugs of the pending and
// approved type proposals, sorted by name and unique: the kinds the LLM is
// told exist. Event types carry their taxonomy description (a name that is
// both a type and an action keeps the type's); actions and proposals carry
// none.
func knownKinds(ctx context.Context, repo eventruleproposalport.Repository, t eventsignals.Taxonomy) ([]appout.KnownEventKind, error) {
	seen := map[string]bool{}
	names := KnownTypeNames(t)
	for _, n := range names {
		seen[n] = true
	}
	for _, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved} {
		ps, err := repo.ListByStatus(ctx, st)
		if err != nil {
			return nil, fmt.Errorf("eventrulecycle: list %s proposals: %w", st, err)
		}
		for _, p := range ps {
			if p.Kind != eventruleproposal.KindType {
				continue
			}
			if n := eventruleproposal.Slug(p.TypeName); n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	kinds := make([]appout.KnownEventKind, 0, len(names))
	for _, n := range names {
		kinds = append(kinds, appout.KnownEventKind{Name: n, Description: t.Description(eventsignals.EventType(n))})
	}
	return kinds, nil
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
	// Framing-only retypes are allowed for generated types only.
	opts.AllowReframe = c.TypeTargets[eventsignals.Action(p.Target)] && p.TargetKind == eventruleproposal.TargetAction
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
	return decide(ctx, d, p.ID, "approve", "", eventruleproposal.Proposal.Approve, now)
}

// Reject rejects the stored proposal p.ID (pending, or approved and not yet
// applied) at now. Any other status returns ErrAlreadyDecided.
func Reject(ctx context.Context, d Deps, p eventruleproposal.Proposal, now time.Time) (eventruleproposal.Proposal, error) {
	return decide(ctx, d, p.ID, "reject", "", eventruleproposal.Proposal.Reject, now)
}

// RejectFrom is Reject only while the stored proposal p.ID is in status
// from, so a reject meant for a pending proposal never drops one approved
// since it was listed. Any other status returns ErrAlreadyDecided.
func RejectFrom(ctx context.Context, d Deps, p eventruleproposal.Proposal, from eventruleproposal.Status, now time.Time) (eventruleproposal.Proposal, error) {
	return decide(ctx, d, p.ID, "reject", from, eventruleproposal.Proposal.Reject, now)
}

// decide re-reads proposal id, so a stale copy never overwrites a decision
// taken elsewhere, normalises its phrase (legacy rows may not be), applies
// transition and saves the result guarded by the status it was read in, so
// a decision landing in between returns ErrAlreadyDecided instead of being
// overwritten. A non-empty from also requires that status; verb names the
// decision in errors.
func decide(ctx context.Context, d Deps, id, verb string, from eventruleproposal.Status, transition func(eventruleproposal.Proposal, time.Time) (eventruleproposal.Proposal, error), now time.Time) (eventruleproposal.Proposal, error) {
	cur, err := d.Proposals.Get(ctx, id)
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("eventrulecycle: reload %s: %w", id, err)
	}
	if from != "" && cur.Status != from {
		return eventruleproposal.Proposal{}, fmt.Errorf("eventrulecycle: %s %s: %w: it is %s, not %s", verb, id, ErrAlreadyDecided, cur.Status, from)
	}
	cur.Phrase = normalizePhrase(cur.Phrase)
	decided, err := transition(cur, now.UTC())
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("eventrulecycle: %s %s: %w: %w", verb, id, ErrAlreadyDecided, err)
	}
	if err := d.Proposals.Transition(ctx, decided, cur.Status); err != nil {
		if errors.Is(err, eventruleproposalport.ErrStatusChanged) {
			return eventruleproposal.Proposal{}, fmt.Errorf("eventrulecycle: %s %s: %w: %w", verb, id, ErrAlreadyDecided, err)
		}
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
	// Check the original before storing anything, so an original decided
	// elsewhere never leaves an approved edit behind.
	cur, err := d.Proposals.Get(ctx, p.ID)
	if err != nil {
		return Assessment{}, fmt.Errorf("eventrulecycle: reload %s: %w", p.ID, err)
	}
	if cur.Status != eventruleproposal.StatusPending {
		return Assessment{}, fmt.Errorf("eventrulecycle: edit %s: %w: it is %s", p.ID, ErrAlreadyDecided, cur.Status)
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
	if _, err := decide(ctx, d, p.ID, "supersede", "", eventruleproposal.Proposal.Reject, now); err != nil {
		return Assessment{}, err
	}
	a.Proposal = approved
	return a, nil
}

// ApplyResult is the outcome of Apply. Candidate is the full generated set
// the gate scored (nil when the gate did not score one); Gated the phrase
// proposals still approved when re-read whose target is known, whose rules
// it added; Applied the phrase proposals and Types the type proposals marked
// applied; Waiting the approved type proposals with no phrase to apply;
// Orphans the approved phrases whose target is neither a declared action
// nor an approved type proposal, which stay approved. Blocked and Reason
// report a gate refusal, after which the proposals stay approved and no file
// is written; Nothing means there was nothing to apply and the gate did not
// run.
type ApplyResult struct {
	Applied   []eventruleproposal.Proposal
	Types     []eventruleproposal.Proposal
	Waiting   []eventruleproposal.Proposal
	Orphans   []eventruleproposal.Proposal
	Gated     []eventruleproposal.Proposal
	Candidate *AppliedSet
	Gate      GateResult
	Blocked   bool
	Reason    string
	Nothing   bool
}

// Apply gates the generated file's rules and types plus toApply (sorted and
// deduplicated) and the approved type proposals they target against the
// clustering benchmark. A phrase is applied when its target is an action or
// signal of the base rules (hand-written plus already applied) or an
// approved type proposal; an approved type proposal is applied with its
// first phrase. When the gate passes it writes the generated file and marks
// those phrases and types applied at now. Each proposal is re-read first and
// skipped unless still approved, so a decision taken since toApply was
// listed is neither overwritten nor written to the file; when none is left
// the result is Nothing. After a passing gate each one (types included) is
// re-read again: if any is no longer approved, ErrApplyStale is returned
// and nothing is written. A failure marking them applied after the file was
// written returns an error saying so; running Apply again finishes it.
func Apply(ctx context.Context, d Deps, toApply []eventruleproposal.Proposal, now time.Time) (ApplyResult, error) {
	var r ApplyResult
	current := make([]eventruleproposal.Proposal, 0, len(toApply))
	for _, p := range toApply {
		cur, err := d.Proposals.Get(ctx, p.ID)
		if err != nil {
			return r, fmt.Errorf("eventrulecycle: reload %s: %w", p.ID, err)
		}
		if cur.Status == eventruleproposal.StatusApproved {
			current = append(current, cur)
		}
	}
	types, err := approvedTypes(ctx, d.Proposals)
	if err != nil {
		return r, err
	}
	if len(current) == 0 {
		r.Nothing, r.Waiting = true, types
		return r, nil
	}
	toApply = current
	sortProposals(toApply)
	if d.Gate == nil || d.ReadApplied == nil || d.WriteApplied == nil || d.BaseRules == nil {
		return r, errors.New("eventrulecycle: Gate, ReadApplied, WriteApplied and BaseRules are required to apply approved rules")
	}
	base, err := d.BaseRules()
	if err != nil {
		return r, fmt.Errorf("eventrulecycle: base rules: %w", err)
	}
	existing, err := d.ReadApplied(d.GeneratedPath)
	if err != nil {
		return r, fmt.Errorf("eventrulecycle: read generated rules: %w", err)
	}
	candidate, gatedTypes := buildCandidate(&r, base.Taxonomy, existing, toApply, types)
	if len(r.Gated) == 0 {
		r.Nothing = true
		return r, nil
	}

	gate, err := d.Gate(ctx, candidate)
	if err != nil {
		return r, fmt.Errorf("eventrulecycle: clustering gate: %w", err)
	}
	r.Candidate, r.Gate = &candidate, gate
	if !gate.OK {
		r.Blocked, r.Reason = true, gate.Reason
		return r, nil
	}
	// The gate can take minutes: a rule or type rejected meanwhile must not
	// be written.
	for _, p := range append(slices.Clone(r.Gated), gatedTypes...) {
		cur, err := d.Proposals.Get(ctx, p.ID)
		if err != nil {
			return r, fmt.Errorf("eventrulecycle: reload %s: %w", p.ID, err)
		}
		if cur.Status != eventruleproposal.StatusApproved {
			return r, ErrApplyStale
		}
	}
	if err := d.WriteApplied(d.GeneratedPath, candidate); err != nil {
		return r, fmt.Errorf("eventrulecycle: write generated rules: %w", err)
	}
	mark := func(p eventruleproposal.Proposal) (eventruleproposal.Proposal, error) {
		applied, err := p.MarkApplied(now.UTC())
		if err == nil {
			err = d.Proposals.Transition(ctx, applied, eventruleproposal.StatusApproved)
		}
		if err != nil {
			return applied, fmt.Errorf("eventrulecycle: the rules file %s was written but marking %s applied failed (%w); run apply again to finish marking", d.GeneratedPath, p.ID, err)
		}
		return applied, nil
	}
	for _, p := range gatedTypes {
		applied, err := mark(p)
		if err != nil {
			return r, err
		}
		r.Types = append(r.Types, applied)
	}
	for _, p := range r.Gated {
		applied, err := mark(p)
		if err != nil {
			return r, err
		}
		r.Applied = append(r.Applied, applied)
	}
	return r, nil
}

// buildCandidate returns existing plus the phrases of toApply whose target
// is known (recorded in r.Gated; the others in r.Orphans) and the approved
// types that one of them targets (returned as gatedTypes; the ones with no
// phrase in r.Waiting). An approved type already in the generated file is
// only marked; one whose name taxonomy declares otherwise is not a type to
// add: its phrases are those of that action.
func buildCandidate(r *ApplyResult, taxonomy eventsignals.Taxonomy, existing AppliedSet, toApply, types []eventruleproposal.Proposal) (AppliedSet, []eventruleproposal.Proposal) {
	actions := map[string]bool{}
	for _, a := range taxonomy.ActionPriority() {
		actions[string(a)] = true
	}
	inFile := map[string]bool{}
	for _, t := range existing.Types {
		actions[t.Name], inFile[t.Name] = true, true
	}
	signals := map[string]bool{}
	for _, s := range taxonomy.SignalPriority() {
		signals[string(s)] = true
	}
	approved := map[string]bool{}
	for _, p := range types {
		if !actions[p.TypeName] {
			approved[p.TypeName] = true
		}
	}

	candidate := AppliedSet{
		Rules: slices.Clone(existing.Rules),
		Types: slices.Clone(existing.Types),
	}
	seen := make(map[string]bool, len(candidate.Rules))
	for _, rule := range candidate.Rules {
		seen[ruleKey(rule)] = true
	}
	withPhrase := map[string]bool{}
	for _, p := range toApply {
		known := false
		switch p.TargetKind {
		case eventruleproposal.TargetAction:
			known = actions[p.Target] || approved[p.Target]
			withPhrase[p.Target] = withPhrase[p.Target] || approved[p.Target]
		case eventruleproposal.TargetSignal:
			known = signals[p.Target]
		}
		if !known {
			r.Orphans = append(r.Orphans, p)
			continue
		}
		r.Gated = append(r.Gated, p)
		rule := AppliedRule{TargetKind: string(p.TargetKind), Target: p.Target, Phrase: normalizePhrase(p.Phrase), ProposalID: p.ID, PromptVersion: p.PromptVersion}
		if seen[ruleKey(rule)] {
			continue
		}
		seen[ruleKey(rule)] = true
		candidate.Rules = append(candidate.Rules, rule)
	}
	var gatedTypes []eventruleproposal.Proposal
	for _, p := range types {
		switch {
		case inFile[p.TypeName]:
			// Written by an apply that failed to mark it: finish marking.
			gatedTypes = append(gatedTypes, p)
		case !approved[p.TypeName]:
		case withPhrase[p.TypeName]:
			gatedTypes = append(gatedTypes, p)
			window := strings.TrimSpace(p.TypeWindow)
			if window == "" {
				window = eventruleproposal.DefaultTypeWindow
			}
			candidate.Types = append(candidate.Types, AppliedType{
				Name: p.TypeName, Description: p.Description, Referential: p.TypeReferential, Window: window,
				ProposalID: p.ID, PromptVersion: p.PromptVersion,
			})
		default:
			r.Waiting = append(r.Waiting, p)
		}
	}
	return candidate, gatedTypes
}

func nowOf(d Deps) func() time.Time {
	if d.Now == nil {
		return time.Now
	}
	return d.Now
}
