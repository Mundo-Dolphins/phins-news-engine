// app/eventrulecycle/run.go
package eventrulecycle

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/eventruleproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const defaultBatchSize = 20

// AppliedRule is one operator-approved phrase in the generated rules file,
// kept free of the infrastructure file format (the cmd glue adapts it).
type AppliedRule struct {
	TargetKind    string // "action" | "signal"
	Target        string
	Phrase        string
	ProposalID    string
	PromptVersion string
}

// Deps are the collaborators of the cycle.
type Deps struct {
	Sampler func(ctx context.Context) ([]eventruleproposal.Sample, error)
	// Control returns a capped sample of recent already-typed articles
	// (optional). Impact runs over it too, so Check can reject a phrase
	// that would change the type of an article that already has one.
	Control   func(ctx context.Context) ([]eventruleproposal.Sample, error)
	LLM       appout.EventRuleDiscoveryLLM
	Proposals eventruleproposalport.Repository
	// BaseRules returns the hand-written rules plus the current generated ones.
	BaseRules func() (eventsignals.Rules, error)
	// Gate evaluates the full candidate generated rule set (already applied
	// plus newly approved) against the clustering benchmark.
	Gate func(ctx context.Context, candidate []AppliedRule) (GateResult, error)
	// ReadApplied returns the rules in the generated file (empty when the
	// file does not exist); WriteApplied replaces the file with rules.
	ReadApplied   func(path string) ([]AppliedRule, error)
	WriteApplied  func(path string, rules []AppliedRule) error
	GeneratedPath string
	Now           func() time.Time
}

// Input is one invocation.
type Input struct {
	BatchSize    int
	CheckOptions eventruleproposal.CheckOptions
	Scanner      *bufio.Scanner
	Out          io.Writer
}

// Result summarises the cycle for the audit report.
type Result struct {
	Sampled, Proposed, Approved, Rejected, Skipped, Applied int
	DiscoveryFailed, Paused, GateBlocked                    bool
	GateReason                                              string
	// GateRan is true when the clustering gate scored a candidate;
	// GateAffected is how many dataset articles changed event type, action
	// or signal under it (0 means the gate was not exercised).
	GateRan      bool
	GateAffected int
}

// Run discovers event rule proposals over a sample of generic articles,
// walks the operator through the pending phrase proposals and applies the
// approved ones when the clustering gate passes. An unreachable LLM is a
// warning, never an error. An empty sample discovers and reviews nothing,
// so the pending backlog is never judged against missing evidence.
func Run(ctx context.Context, d Deps, in Input) (Result, error) {
	var res Result
	now := d.Now
	if now == nil {
		now = time.Now
	}
	if in.Out == nil {
		in.Out = io.Discard
	}

	sample, err := d.Sampler(ctx)
	if err != nil {
		return res, fmt.Errorf("eventrulecycle: sample: %w", err)
	}
	res.Sampled = len(sample)
	if len(sample) == 0 {
		fmt.Fprintln(in.Out, "event rules: no generic articles in the sample; nothing discovered or reviewed this cycle.")
		return res, nil
	}
	base, err := d.BaseRules()
	if err != nil {
		return res, fmt.Errorf("eventrulecycle: base rules: %w", err)
	}
	var control []eventruleproposal.Sample
	if d.Control != nil {
		if control, err = d.Control(ctx); err != nil {
			return res, fmt.Errorf("eventrulecycle: typed control sample: %w", err)
		}
	}

	topics, phrases, failed, calls, firstErr := discover(ctx, d.LLM, sample, in.BatchSize)
	fmt.Fprintf(in.Out, "event rules: %d generic articles sampled, %d LLM calls (%d failed)\n", res.Sampled, calls, failed)
	if calls > 0 && failed == calls {
		res.DiscoveryFailed = true
		fmt.Fprintf(in.Out, "WARNING: event rule discovery is unavailable (%v); no new event rules this cycle.\n", firstErr)
		return res, nil
	}
	if failed > 0 {
		fmt.Fprintf(in.Out, "WARNING: %d of %d discovery calls failed (%v); results of the other calls are kept.\n", failed, calls, firstErr)
	}

	n, typesNow, err := persistProposals(ctx, d.Proposals, base, sample, topics, phrases, now().UTC())
	res.Proposed = n
	if err != nil {
		return res, err
	}
	fmt.Fprintf(in.Out, "event rules: %d proposals created or updated\n", res.Proposed)

	s := &session{
		ctx: ctx, d: d, in: in, base: base, sample: sample, control: control, text: sampleText(sample),
		now: now, res: &res, approvedNow: map[string]bool{},
	}
	if err := s.review(); err != nil {
		return res, err
	}
	if err := s.apply(); err != nil {
		return res, err
	}
	return res, listTypes(ctx, d.Proposals, in.Out, typesNow)
}

func discover(ctx context.Context, llm appout.EventRuleDiscoveryLLM, sample []eventruleproposal.Sample, batchSize int) (topics []appout.EventTopicProposal, phrases []appout.EventPhraseProposal, failed, calls int, firstErr error) {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	actions := make([]string, 0)
	for _, a := range eventsignals.AllActions() {
		actions = append(actions, string(a))
	}
	signals := make([]string, 0)
	for _, s := range eventsignals.AllContentSignals() {
		signals = append(signals, string(s))
	}
	note := func(err error) {
		failed++
		if firstErr == nil {
			firstErr = err
		}
	}
	for start := 0; start < len(sample); start += batchSize {
		batch := sample[start:min(start+batchSize, len(sample))]
		arts := make([]appout.EventRuleArticle, 0, len(batch))
		for _, s := range batch {
			arts = append(arts, appout.EventRuleArticle{ArticleID: s.Article.ID, Title: s.Article.Title, Summary: s.Article.Summary})
		}
		calls += 2
		if ts, err := llm.DiscoverTopics(ctx, arts); err != nil {
			note(err)
		} else {
			topics = append(topics, ts...)
		}
		if ps, err := llm.DiscoverPhrases(ctx, arts, actions, signals); err != nil {
			note(err)
		} else {
			phrases = append(phrases, ps...)
		}
	}
	return topics, phrases, failed, calls, firstErr
}

func normalizePhrase(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// knownTarget reports whether the LLM's target exists in the closed lists.
func knownTarget(kind, target string) (eventruleproposal.TargetKind, bool) {
	switch eventruleproposal.TargetKind(kind) {
	case eventruleproposal.TargetAction:
		for _, a := range eventsignals.AllActions() {
			if string(a) == target {
				return eventruleproposal.TargetAction, true
			}
		}
	case eventruleproposal.TargetSignal:
		for _, s := range eventsignals.AllContentSignals() {
			if string(s) == target {
				return eventruleproposal.TargetSignal, true
			}
		}
	}
	return "", false
}

// persistProposals validates and upserts the LLM output as pending
// proposals and returns how many were stored and the IDs of the type
// proposals stored or updated. A phrase already in any bucket of base (the
// hand-written plus the current generated rules) is not stored.
func persistProposals(ctx context.Context, repo eventruleproposalport.Repository, base eventsignals.Rules, sample []eventruleproposal.Sample, topics []appout.EventTopicProposal, phrases []appout.EventPhraseProposal, now time.Time) (int, map[string]bool, error) {
	text := sampleText(sample)
	n := 0
	typesNow := map[string]bool{}
	for _, lp := range phrases {
		target := strings.TrimSpace(lp.Target)
		tk, ok := knownTarget(strings.TrimSpace(lp.TargetKind), target)
		phrase := normalizePhrase(lp.Phrase)
		if !ok || phrase == "" {
			continue
		}
		if eventruleproposal.VocabularyBucket(base, phrase) != "" {
			continue
		}
		var evidence []string
		for _, id := range lp.ArticleIDs {
			if t, ok := text[id]; ok && strings.Contains(t, phrase) {
				evidence = append(evidence, id)
			}
		}
		if len(evidence) == 0 {
			continue
		}
		if _, err := repo.Upsert(ctx, eventruleproposal.Proposal{
			Kind: eventruleproposal.KindPhrase, TargetKind: tk, Target: target, Phrase: phrase,
			EvidenceIDs: evidence, Status: eventruleproposal.StatusPending,
			PromptVersion: EventRulesPromptVersion, CreatedAt: now,
		}); err != nil {
			return n, typesNow, fmt.Errorf("eventrulecycle: upsert phrase %q: %w", phrase, err)
		}
		n++
	}
	for _, tp := range topics {
		name := normalizePhrase(tp.Name)
		if name == "" {
			continue
		}
		var evidence []string
		for _, id := range tp.ArticleIDs {
			if _, ok := text[id]; ok {
				evidence = append(evidence, id)
			}
		}
		stored, err := repo.Upsert(ctx, eventruleproposal.Proposal{
			Kind: eventruleproposal.KindType, TypeName: name, Description: strings.TrimSpace(tp.Description),
			Target: strings.TrimSpace(tp.SuggestedTarget), EvidenceIDs: evidence,
			Status: eventruleproposal.StatusPending, PromptVersion: EventRulesPromptVersion, CreatedAt: now,
		})
		if err != nil {
			return n, typesNow, fmt.Errorf("eventrulecycle: upsert topic %q: %w", name, err)
		}
		if stored.Status == eventruleproposal.StatusPending {
			typesNow[stored.ID] = true
		}
		n++
	}
	return n, typesNow, nil
}

func sampleText(sample []eventruleproposal.Sample) map[string]string {
	text := make(map[string]string, len(sample))
	for _, s := range sample {
		text[s.Article.ID] = strings.ToLower(s.Article.Title + " " + s.Article.Summary)
	}
	return text
}

func sortProposals(ps []eventruleproposal.Proposal) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.TargetKind != b.TargetKind {
			return a.TargetKind < b.TargetKind
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Phrase < b.Phrase
	})
}

func phrasesByStatus(ctx context.Context, repo eventruleproposalport.Repository, st eventruleproposal.Status) ([]eventruleproposal.Proposal, error) {
	all, err := repo.ListByStatus(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("eventrulecycle: list %s proposals: %w", st, err)
	}
	var out []eventruleproposal.Proposal
	for _, p := range all {
		if p.Kind == eventruleproposal.KindPhrase {
			out = append(out, p)
		}
	}
	sortProposals(out)
	return out, nil
}

// session is the state of one Run's interactive part.
type session struct {
	ctx    context.Context
	d      Deps
	in     Input
	base   eventsignals.Rules
	sample []eventruleproposal.Sample
	// control is the typed control sample: Impact runs over it as well so
	// Retyped can count already-typed articles a phrase would change.
	control []eventruleproposal.Sample
	text    map[string]string // article ID -> lowercased title + summary
	now     func() time.Time
	res     *Result
	// approvedNow holds the IDs approved during this run; other approved
	// proposals are carried over and re-presented before the gate.
	approvedNow map[string]bool
}

// readLine reads one operator line. ok is false on a clean end of input; a
// read failure is returned as an error, never treated as a quit.
func (s *session) readLine() (line string, ok bool, err error) {
	if s.in.Scanner == nil {
		return "", false, errors.New("eventrulecycle: an input scanner is required to prompt the operator")
	}
	if !s.in.Scanner.Scan() {
		if err := s.in.Scanner.Err(); err != nil {
			return "", false, fmt.Errorf("eventrulecycle: read operator input: %w", err)
		}
		return "", false, nil
	}
	return s.in.Scanner.Text(), true, nil
}

// impact measures p over the generic sample plus the typed control sample.
func (s *session) impact(p eventruleproposal.Proposal) eventruleproposal.ImpactResult {
	all := s.sample
	if len(s.control) > 0 {
		all = append(append([]eventruleproposal.Sample(nil), s.sample...), s.control...)
	}
	return eventruleproposal.Impact(s.base, p, all, s.now().UTC())
}

func (s *session) save(p eventruleproposal.Proposal, verb string) error {
	if err := s.d.Proposals.Save(s.ctx, p); err != nil {
		return fmt.Errorf("eventrulecycle: %s %s: %w", verb, p.ID, err)
	}
	return nil
}

func (s *session) pause() {
	s.res.Paused = true
	fmt.Fprintf(s.in.Out, "\nEvent rule review paused. approved: %d, rejected: %d, skipped: %d\n", s.res.Approved, s.res.Rejected, s.res.Skipped)
}

// onlyNoMatches reports whether the sample merely lacks evidence for the
// phrase, which is no reason to reject it.
func onlyNoMatches(v eventruleproposal.Verdict) bool {
	return len(v.Reasons) == 1 && v.Reasons[0] == eventruleproposal.ReasonNoMatches
}

func (s *session) review() error {
	pending, err := phrasesByStatus(s.ctx, s.d.Proposals, eventruleproposal.StatusPending)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Fprintln(s.in.Out, "No pending event rule phrase proposals.")
		return nil
	}
	for i, listed := range pending {
		// Re-read: an edit earlier in this run may already have decided it.
		p, err := s.d.Proposals.Get(s.ctx, listed.ID)
		if err != nil {
			return fmt.Errorf("eventrulecycle: reload %s: %w", listed.ID, err)
		}
		if p.Status != eventruleproposal.StatusPending {
			continue
		}
		p.Phrase = normalizePhrase(p.Phrase)
		impact := s.impact(p)
		verdict := eventruleproposal.Check(p, impact, len(s.sample), s.in.CheckOptions)
		fmt.Fprintf(s.in.Out, "\n--- Event rule proposal %d/%d ---\n", i+1, len(pending))
		if onlyNoMatches(verdict) {
			fmt.Fprintf(s.in.Out, "%s %s · %q: no evidence in this sample; left pending.\n", p.TargetKind, p.Target, p.Phrase)
			continue
		}
		if !verdict.OK {
			rejected, err := p.Reject(s.now().UTC())
			if err != nil {
				return fmt.Errorf("eventrulecycle: auto-reject %s: %w", p.ID, err)
			}
			if err := s.save(rejected, "auto-reject"); err != nil {
				return err
			}
			fmt.Fprintf(s.in.Out, "%s %s · %q auto-rejected: %s\n", p.TargetKind, p.Target, p.Phrase, strings.Join(verdict.Reasons, "; "))
			s.res.Rejected++
			continue
		}
		printProposal(s.in.Out, p, impact)
		for {
			fmt.Fprint(s.in.Out, "Action: [a]ccept, [r]eject, [e]dit phrase, [s]kip, [q]uit: ")
			line, ok, err := s.readLine()
			if err != nil {
				return err
			}
			if !ok {
				s.pause()
				return nil
			}
			advance, stop, err := s.handle(p, strings.ToLower(strings.TrimSpace(line)))
			if err != nil {
				return err
			}
			if stop {
				s.pause()
				return nil
			}
			if advance {
				break
			}
		}
	}
	fmt.Fprintf(s.in.Out, "Event rule review complete. approved: %d, rejected: %d, skipped: %d\n", s.res.Approved, s.res.Rejected, s.res.Skipped)
	return nil
}

// handle applies one action; advance moves to the next proposal, stop pauses.
func (s *session) handle(p eventruleproposal.Proposal, action string) (advance, stop bool, err error) {
	switch action {
	case "a", "accept", "approve":
		approved, err := p.Approve(s.now().UTC())
		if err != nil {
			return false, false, fmt.Errorf("eventrulecycle: approve %s: %w", p.ID, err)
		}
		if err := s.save(approved, "approve"); err != nil {
			return false, false, err
		}
		s.approvedNow[p.ID] = true
		fmt.Fprintf(s.in.Out, "Approved %q.\n", p.Phrase)
		s.res.Approved++
		return true, false, nil
	case "r", "reject":
		rejected, err := p.Reject(s.now().UTC())
		if err != nil {
			return false, false, fmt.Errorf("eventrulecycle: reject %s: %w", p.ID, err)
		}
		if err := s.save(rejected, "reject"); err != nil {
			return false, false, err
		}
		fmt.Fprintf(s.in.Out, "Rejected %q.\n", p.Phrase)
		s.res.Rejected++
		return true, false, nil
	case "e", "edit":
		return s.edit(p)
	case "s", "skip", "":
		s.res.Skipped++
		return true, false, nil
	case "q", "quit":
		return false, true, nil
	default:
		fmt.Fprintln(s.in.Out, "Unknown action. Choose a, r, e, s, or q.")
		return false, false, nil
	}
}

// edit reads a replacement phrase, re-checks it, and on success approves
// the edited rule as its own proposal and rejects the original as
// superseded. A failing edit leaves the original pending and re-prompts.
func (s *session) edit(p eventruleproposal.Proposal) (advance, stop bool, err error) {
	fmt.Fprint(s.in.Out, "New phrase: ")
	line, ok, err := s.readLine()
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, true, nil
	}
	edited := p
	edited.ID = ""
	edited.Phrase = normalizePhrase(line)
	edited.CreatedAt = s.now().UTC()
	edited.DecidedAt = time.Time{}
	if edited.Phrase == "" || edited.Phrase == p.Phrase {
		fmt.Fprintln(s.in.Out, "Phrase unchanged; choose another action.")
		return false, false, nil
	}
	edited.EvidenceIDs = nil
	for _, id := range p.EvidenceIDs {
		if strings.Contains(s.text[id], edited.Phrase) {
			edited.EvidenceIDs = append(edited.EvidenceIDs, id)
		}
	}
	if len(edited.EvidenceIDs) == 0 {
		fmt.Fprintf(s.in.Out, "No cited article contains %q; choose another action.\n", edited.Phrase)
		return false, false, nil
	}
	impact := s.impact(edited)
	if v := eventruleproposal.Check(edited, impact, len(s.sample), s.in.CheckOptions); !v.OK {
		fmt.Fprintf(s.in.Out, "Edited phrase %q fails the checks: %s\n", edited.Phrase, strings.Join(v.Reasons, "; "))
		return false, false, nil
	}
	stored, err := s.d.Proposals.Upsert(s.ctx, edited)
	if err != nil {
		return false, false, fmt.Errorf("eventrulecycle: store edited phrase %q: %w", edited.Phrase, err)
	}
	if stored.Status != eventruleproposal.StatusPending {
		fmt.Fprintf(s.in.Out, "Phrase %q was already %s; choose another action.\n", stored.Phrase, stored.Status)
		return false, false, nil
	}
	approved, err := stored.Approve(s.now().UTC())
	if err != nil {
		return false, false, fmt.Errorf("eventrulecycle: approve %s: %w", stored.ID, err)
	}
	if err := s.save(approved, "approve"); err != nil {
		return false, false, err
	}
	s.approvedNow[stored.ID] = true
	superseded, err := p.Reject(s.now().UTC())
	if err != nil {
		return false, false, fmt.Errorf("eventrulecycle: supersede %s: %w", p.ID, err)
	}
	if err := s.save(superseded, "supersede"); err != nil {
		return false, false, err
	}
	printImpact(s.in.Out, impact)
	fmt.Fprintf(s.in.Out, "Approved edited phrase %q (original %q superseded).\n", approved.Phrase, p.Phrase)
	s.res.Approved++
	return true, false, nil
}

func printProposal(w io.Writer, p eventruleproposal.Proposal, impact eventruleproposal.ImpactResult) {
	fmt.Fprintf(w, "%s %s · phrase %q · evidence %s\n", p.TargetKind, p.Target, p.Phrase, strings.Join(p.EvidenceIDs, ", "))
	printImpact(w, impact)
}

func printImpact(w io.Writer, impact eventruleproposal.ImpactResult) {
	types := make([]string, 0, len(impact.Moved))
	for t := range impact.Moved {
		types = append(types, string(t))
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(w, "Impact: %d generic → %s\n", impact.Moved[eventsignals.EventType(t)], t)
	}
	for _, title := range impact.Examples {
		fmt.Fprintf(w, "  e.g. %s\n", title)
	}
}

func ruleKey(r AppliedRule) string {
	return r.TargetKind + "|" + r.Target + "|" + normalizePhrase(r.Phrase)
}

// confirmCarriedOver re-presents approved proposals from earlier runs that
// were never applied (for example because a gate blocked them) and returns
// the ones the operator keeps. After a pause none are presented or kept.
func (s *session) confirmCarriedOver(carried []eventruleproposal.Proposal) ([]eventruleproposal.Proposal, error) {
	if len(carried) == 0 || s.res.Paused {
		return nil, nil
	}
	var kept []eventruleproposal.Proposal
	for i, p := range carried {
		fmt.Fprintf(s.in.Out, "\n--- Event rule %d/%d: approved, not yet applied ---\n", i+1, len(carried))
		fmt.Fprintf(s.in.Out, "%s · ", p.ID)
		printProposal(s.in.Out, p, s.impact(p))
		for decided := false; !decided; {
			fmt.Fprint(s.in.Out, "Action: [k]eep for this apply, [r]eject, [q]uit: ")
			line, ok, err := s.readLine()
			if err != nil {
				return nil, err
			}
			if !ok {
				s.pause()
				return kept, nil
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "k", "keep":
				kept = append(kept, p)
				decided = true
			case "r", "reject":
				rejected, err := p.Reject(s.now().UTC())
				if err != nil {
					return nil, fmt.Errorf("eventrulecycle: reject %s: %w", p.ID, err)
				}
				if err := s.save(rejected, "reject"); err != nil {
					return nil, err
				}
				fmt.Fprintf(s.in.Out, "Rejected %q.\n", p.Phrase)
				s.res.Rejected++
				decided = true
			case "q", "quit":
				s.pause()
				return kept, nil
			default:
				fmt.Fprintln(s.in.Out, "Unknown action. Choose k, r, or q.")
			}
		}
	}
	return kept, nil
}

// apply gates and writes the phrase proposals approved in this run plus
// the carried-over ones the operator kept.
func (s *session) apply() error {
	approved, err := phrasesByStatus(s.ctx, s.d.Proposals, eventruleproposal.StatusApproved)
	if err != nil {
		return err
	}
	var fresh, carried []eventruleproposal.Proposal
	for _, p := range approved {
		if s.approvedNow[p.ID] {
			fresh = append(fresh, p)
		} else {
			carried = append(carried, p)
		}
	}
	kept, err := s.confirmCarriedOver(carried)
	if err != nil {
		return err
	}
	toApply := append(kept, fresh...)
	sortProposals(toApply)
	if len(toApply) == 0 {
		return nil
	}
	d := s.d
	if d.Gate == nil || d.ReadApplied == nil || d.WriteApplied == nil {
		return errors.New("eventrulecycle: Gate, ReadApplied and WriteApplied are required to apply approved rules")
	}
	existing, err := d.ReadApplied(d.GeneratedPath)
	if err != nil {
		return fmt.Errorf("eventrulecycle: read generated rules: %w", err)
	}
	candidate := append([]AppliedRule(nil), existing...)
	seen := make(map[string]bool, len(candidate))
	for _, r := range candidate {
		seen[ruleKey(r)] = true
	}
	for _, p := range toApply {
		r := AppliedRule{TargetKind: string(p.TargetKind), Target: p.Target, Phrase: normalizePhrase(p.Phrase), ProposalID: p.ID, PromptVersion: p.PromptVersion}
		if seen[ruleKey(r)] {
			continue
		}
		seen[ruleKey(r)] = true
		candidate = append(candidate, r)
	}

	gate, err := d.Gate(s.ctx, candidate)
	if err != nil {
		return fmt.Errorf("eventrulecycle: clustering gate: %w", err)
	}
	s.res.GateRan, s.res.GateAffected = true, gate.Affected
	fmt.Fprintf(s.in.Out, "Clustering gate: %s\n", GateExercise(gate.Affected))
	if !gate.OK {
		s.res.GateBlocked = true
		s.res.GateReason = gate.Reason
		fmt.Fprintf(s.in.Out, "Clustering gate blocked %d approved rule(s): %s\n", len(toApply), gate.Reason)
		for _, p := range toApply {
			fmt.Fprintf(s.in.Out, "  - %s %s %s %q\n", p.ID, p.TargetKind, p.Target, p.Phrase)
		}
		fmt.Fprintln(s.in.Out, "They stay approved; no rules file was written. Reject the offender in the next run.")
		return nil
	}
	if err := d.WriteApplied(d.GeneratedPath, candidate); err != nil {
		return fmt.Errorf("eventrulecycle: write generated rules: %w", err)
	}
	for _, p := range toApply {
		applied, err := p.MarkApplied(s.now().UTC())
		if err != nil {
			return fmt.Errorf("eventrulecycle: mark applied %s: %w", p.ID, err)
		}
		if err := s.save(applied, "mark applied"); err != nil {
			return err
		}
		s.res.Applied++
	}
	fmt.Fprintf(s.in.Out, "Clustering gate passed; %d rule(s) applied to %s\n", s.res.Applied, d.GeneratedPath)
	return nil
}

// GateExercise describes how many gold dataset articles a candidate changed.
func GateExercise(affected int) string {
	if affected == 0 {
		return "gate not exercised (0 dataset articles affected)"
	}
	return fmt.Sprintf("%d dataset article(s) affected", affected)
}

// listTypes prints the type proposals stored or updated in this run (ids in
// typesNow) and counts the older pending and the approved ones; they are
// never applied here.
func listTypes(ctx context.Context, repo eventruleproposalport.Repository, w io.Writer, typesNow map[string]bool) error {
	count := func(st eventruleproposal.Status) ([]eventruleproposal.Proposal, error) {
		ps, err := repo.ListByStatus(ctx, st)
		if err != nil {
			return nil, fmt.Errorf("eventrulecycle: list %s proposals: %w", st, err)
		}
		var types []eventruleproposal.Proposal
		for _, p := range ps {
			if p.Kind == eventruleproposal.KindType {
				types = append(types, p)
			}
		}
		return types, nil
	}
	pending, err := count(eventruleproposal.StatusPending)
	if err != nil {
		return err
	}
	approved, err := count(eventruleproposal.StatusApproved)
	if err != nil {
		return err
	}
	var current []eventruleproposal.Proposal
	older := 0
	for _, p := range pending {
		if typesNow[p.ID] {
			current = append(current, p)
		} else {
			older++
		}
	}
	if len(current) > 0 {
		fmt.Fprintln(w, "Event type proposals from this run (not applied automatically; decide with `events type approve|reject <id>`):")
		for _, p := range current {
			fmt.Fprintf(w, "  - %s %s: %s (evidence %s)\n", p.ID, p.TypeName, p.Description, strings.Join(p.EvidenceIDs, ", "))
		}
	}
	if older > 0 {
		fmt.Fprintf(w, "%d older pending type proposal(s) not shown; see `events type list`.\n", older)
	}
	if len(approved) > 0 {
		fmt.Fprintf(w, "%d approved type proposal(s) await a code change; see `events brief <id>`.\n", len(approved))
	}
	return nil
}
