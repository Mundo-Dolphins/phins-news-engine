package clustering

import (
	"fmt"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// isReferential reports whether tax flags t as referential: an EventType
// assigned from how an article *frames* its coverage of an event
// (confirming, correcting, rumoring, analyzing, reacting to, or quoting
// it, today) rather than from what kind of event occurred. An article with
// one of these EventTypes typically describes the same underlying event
// as an earlier transaction/injury/draft/game/practice report even though
// its own EventType differs from that report's — Taxonomy.Classify lets a
// ContentSignal-derived framing win over the action-derived category, so
// a rumor's own EventType is "rumor", not "transaction", and its later
// confirmation's EventType is "confirmation", not "transaction".
// compareEventType (via Compare) treats a referential EventType on either
// side as compatible with any EventType on the other side; it never
// disqualifies a same-event join purely because a referential article's
// EventType differs from the original report's.
func isReferential(tax eventsignals.Taxonomy, t eventsignals.EventType) bool {
	return tax.HasFlag(t, eventsignals.FlagReferential)
}

// eventTypeCompatible reports whether a and b are compatible enough to be
// the same underlying event: identical, or either side is a referential
// framing (see isReferential) of some other event.
func eventTypeCompatible(tax eventsignals.Taxonomy, a, b eventsignals.EventType) bool {
	return a == b || isReferential(tax, a) || isReferential(tax, b)
}

// detectConflict reports whether incoming and cand are hard-disqualified
// from being the same event, regardless of any other shared signal
// (including a shared PrimaryEntity) — and, if so, one Evidence string
// naming why. These are the plan's required "must not join" cases:
// differing primary subjects, differing named opponents, an
// action/event-type combination that shares no compatible category, or an
// event date outside both sides' event windows.
func detectConflict(incoming, cand eventsignals.Representation, tax eventsignals.Taxonomy) (bool, Evidence) {
	if incoming.PrimaryEntity != "" && cand.PrimaryEntity != "" && incoming.PrimaryEntity != cand.PrimaryEntity {
		return true, Evidence(fmt.Sprintf("conflict: primary entity %q vs %q", incoming.PrimaryEntity, cand.PrimaryEntity))
	}

	if incoming.Opponent != "" && cand.Opponent != "" && incoming.Opponent != cand.Opponent {
		return true, Evidence(fmt.Sprintf("conflict: opponent %q vs %q", incoming.Opponent, cand.Opponent))
	}

	actionMatches := incoming.Action != "" && incoming.Action == cand.Action
	if !eventTypeCompatible(tax, incoming.EventType, cand.EventType) && !actionMatches {
		return true, Evidence(fmt.Sprintf(
			"conflict: event type %q vs %q and action %q vs %q neither match nor are referential framings of each other",
			incoming.EventType, cand.EventType, incoming.Action, cand.Action,
		))
	}

	if window, diff, withinWindow := eventDateWindow(incoming, cand); !withinWindow {
		return true, Evidence(fmt.Sprintf(
			"conflict: event dates %s and %s are %s apart, outside the %s event window",
			incoming.EventDate.Format(time.RFC3339), cand.EventDate.Format(time.RFC3339), diff, window,
		))
	}

	return false, ""
}

// eventDateWindow computes the single window/diff arithmetic that both
// detectConflict's gate and Compare's dateWindowMatched signal need: the
// window used (the larger of incoming and cand's EventWindow, zero if
// neither configured one), how far apart the two EventDates actually are,
// and whether that gap falls within the window. When window is zero (no
// EventWindow configured on either side), withinWindow is always true — no
// temporal constraint is enforced. Kept as the one place this arithmetic
// lives so a future change to window tolerance can't silently create two
// different notions of "in window" between the gate and the score.
func eventDateWindow(incoming, cand eventsignals.Representation) (window, diff time.Duration, withinWindow bool) {
	window = incoming.EventWindow
	if cand.EventWindow > window {
		window = cand.EventWindow
	}
	diff = incoming.EventDate.Sub(cand.EventDate)
	if diff < 0 {
		diff = -diff
	}
	withinWindow = window <= 0 || diff <= window
	return window, diff, withinWindow
}

// weightedSignal is one named, weighted input to Compare's score: whether
// it applies to this pair at all, and if so whether it matched.
type weightedSignal struct {
	weight     float64
	applicable bool
	matched    bool
	evidence   string
}

// Compare scores the incoming article's Representation against one
// Candidate, producing the Comparison DecideJoin uses to rank cand's
// cluster against every other candidate cluster. Compare is pure: it
// consults only its arguments, never wall-clock time or package state.
//
// Compare first runs detectConflict: if incoming and cand.Representation
// are hard-disqualified (differing primary entity, differing opponent, an
// incompatible action/event-type pairing, or a date outside both sides'
// event windows), Compare returns Score 0 and RelationDifferent
// immediately — a shared PrimaryEntity alone is never enough to overcome
// a conflict on these signals. Otherwise Compare computes a weighted
// average over every Config weight applicable to this pair (see Config's
// per-weight doc comments) and classifies Relation from
// Candidate.DuplicateRelation (if the caller supplied a dedup hint) or
// from whether the incoming article contributes new information over
// cand.
func Compare(incoming eventsignals.Representation, cand Candidate, cfg Config) Comparison {
	c := Comparison{ArticleID: cand.ArticleID, ClusterID: cand.ClusterID}
	rep := cand.Representation

	if conflicted, reason := detectConflict(incoming, rep, cfg.Taxonomy); conflicted {
		c.Relation = RelationDifferent
		c.Score = 0
		c.Evidence = []Evidence{reason}
		return c
	}

	// Sparse representations are routing context, not event identity. A
	// shared team/player plus a generic event type used to match every
	// applicable signal and therefore score a perfect 1.0. The same issue
	// affected team-level quotes and previews whenever the extractor did not
	// find a concrete subject. Require an event-level anchor before scoring;
	// exact/near-copy hints remain sufficient on their own.
	if !hasSufficientEventIdentity(incoming, rep, cand.DuplicateRelation, cfg.Taxonomy) {
		c.Relation = RelationDifferent
		c.Score = 0
		c.Evidence = []Evidence{
			"insufficient event identity: shared routing context is not a shared event",
		}
		return c
	}

	entityApplicable := incoming.PrimaryEntity != "" && rep.PrimaryEntity != ""
	entityMatched := entityApplicable && incoming.PrimaryEntity == rep.PrimaryEntity

	actionApplicable := incoming.Action != "" && rep.Action != ""
	actionMatched := actionApplicable && incoming.Action == rep.Action

	eventTypeMatched := eventTypeCompatible(cfg.Taxonomy, incoming.EventType, rep.EventType)

	opponentApplicable := incoming.Opponent != "" && rep.Opponent != ""
	// detectConflict already ruled out a mismatch, so if both sides named
	// an opponent it necessarily matches.
	opponentMatched := opponentApplicable

	// detectConflict already disqualified any pair whose dates fall outside
	// the window (via the same eventDateWindow arithmetic), so
	// dateWindowMatched is structurally guaranteed true for every
	// Comparison that reaches this point — see DateWindowMatchWeight's doc
	// comment on Config for why it is therefore excluded from Score below.
	window, _, dateWindowMatched := eventDateWindow(incoming, rep)

	sourceApplicable := incoming.SourceID != "" && rep.SourceID != ""
	sourceMatched := sourceApplicable && incoming.SourceID != rep.SourceID

	noveltyApplicable := actionApplicable && rep.Action != ""
	noveltyMatched := noveltyApplicable && incoming.Action != rep.Action

	// DateWindowMatchWeight is deliberately not one of these: see its doc
	// comment on Config for why a signal detectConflict already guarantees
	// true cannot discriminate among surviving candidates, and would only
	// dilute the six signals below that can.
	signals := []weightedSignal{
		{
			cfg.EntityMatchWeight, entityApplicable, entityMatched,
			fmt.Sprintf("entity: both articles' primary entity is %q", incoming.PrimaryEntity),
		},
		{
			cfg.ActionMatchWeight, actionApplicable, actionMatched,
			fmt.Sprintf("action: both articles have action %q", incoming.Action),
		},
		{
			cfg.EventTypeMatchWeight, true, eventTypeMatched,
			fmt.Sprintf("event type: %q compatible with %q", incoming.EventType, rep.EventType),
		},
		{
			cfg.OpponentMatchWeight, opponentApplicable, opponentMatched,
			fmt.Sprintf("opponent: both articles name opponent %q", incoming.Opponent),
		},
		{
			cfg.SourcePriorityWeight, sourceApplicable, sourceMatched,
			"source: independent corroboration from a different source",
		},
		{
			cfg.NoveltyMatchWeight, noveltyApplicable, noveltyMatched,
			fmt.Sprintf("novelty: action progressed from %q to %q", rep.Action, incoming.Action),
		},
	}

	var numerator, denominator float64
	var evidence []Evidence
	for _, s := range signals {
		if !s.applicable || s.weight <= 0 {
			continue
		}
		denominator += s.weight
		if s.matched {
			numerator += s.weight
			evidence = append(evidence, Evidence(s.evidence))
		}
	}
	if denominator > 0 {
		c.Score = numerator / denominator
	}
	// dateWindowMatched never varies (see above), so it never affects
	// Score/numerator/denominator; DateWindowMatchWeight only toggles
	// whether Compare cites it as evidence, mirroring every other weight's
	// "0 disables this signal" convention even though here that "signal"
	// is evidence-only.
	if cfg.DateWindowMatchWeight > 0 {
		evidence = append(evidence, Evidence(fmt.Sprintf("date window: event dates within %s", window)))
	}

	c.EntityMatch = entityMatched
	c.ActionMatch = actionMatched
	c.EventTypeMatch = eventTypeMatched
	c.OpponentMatch = opponentMatched
	c.DateWindowMatch = dateWindowMatched
	c.SourceMatch = sourceMatched
	c.NoveltyMatch = noveltyMatched
	c.Evidence = evidence

	switch {
	case cand.DuplicateRelation == RelationExactDuplicate:
		c.Relation = RelationExactDuplicate
		c.Evidence = append(c.Evidence, Evidence(fmt.Sprintf(
			"dedup: candidate flagged exact_duplicate (score %.2f)", cand.DuplicateScore,
		)))
	case cand.DuplicateRelation == RelationNearCopy:
		c.Relation = RelationNearCopy
		c.Evidence = append(c.Evidence, Evidence(fmt.Sprintf(
			"dedup: candidate flagged near_copy (score %.2f)", cand.DuplicateScore,
		)))
	case noveltyMatched, isUpdateFraming(incoming, cfg.Taxonomy):
		c.Relation = RelationUpdate
	default:
		c.Relation = RelationSameEvent
	}

	return c
}

// hasSufficientEventIdentity prevents normalized scores from turning a
// handful of weak-but-matching signals into a perfect match. A concrete
// person plus an action can identify an event. Team-level coverage needs a
// game opponent and action because the team name alone is present in almost
// every article. Referential confirmation/correction framing may link a
// concrete subject to the original action even though the actions differ.
func hasSufficientEventIdentity(incoming, candidate eventsignals.Representation, duplicateRelation Relation, tax eventsignals.Taxonomy) bool {
	if duplicateRelation == RelationExactDuplicate || duplicateRelation == RelationNearCopy {
		return true
	}

	if incoming.EventType == eventsignals.EventTypeGeneric || candidate.EventType == eventsignals.EventTypeGeneric {
		return false
	}

	entityMatches := incoming.PrimaryEntity != "" && incoming.PrimaryEntity == candidate.PrimaryEntity
	if !entityMatches {
		return false
	}

	actionMatches := incoming.Action != "" && incoming.Action == candidate.Action
	// A shared EventType only signals a single incident progressing through
	// life-cycle actions (e.g. injury: injure -> return, transaction: sign ->
	// extend) for substantive EventTypes. Quote/analysis/reaction/rumor/
	// confirmation/correction are framing categories an article lands in
	// independent of its subject — two different topics about the same
	// person can both be classified "quote" (each merely contains reported
	// speech), so treating "same referential EventType, different action" as
	// sufficient identity let unrelated same-person articles merge in
	// production. Referential framing still grants sufficiency, but only via
	// isUpdateFraming below, which requires an explicit confirmation/
	// correction cue rather than a bare shared category.
	sameEventTypeProgression := incoming.Action != "" && candidate.Action != "" &&
		incoming.EventType == candidate.EventType && !isReferential(tax, incoming.EventType)
	concreteSubject := incoming.PrimaryEntity != incoming.PrimaryTeam && candidate.PrimaryEntity != candidate.PrimaryTeam
	if concreteSubject && (actionMatches ||
		sameEventTypeProgression ||
		isUpdateFraming(incoming, tax) || isUpdateFraming(candidate, tax)) {
		return true
	}

	return actionMatches && incoming.EventType == candidate.EventType &&
		tax.HasFlag(incoming.EventType, eventsignals.FlagTeamLevelNeedsOpponent) &&
		incoming.Opponent != "" && incoming.Opponent == candidate.Opponent
}

// isUpdateFraming reports whether rep explicitly frames itself as
// contributing new information about an earlier report — a confirmation
// or correction, today — as opposed to being a plain restatement: its
// EventType, or the EventType its ContentSignal maps to, carries tax's
// update_framing flag.
func isUpdateFraming(rep eventsignals.Representation, tax eventsignals.Taxonomy) bool {
	return tax.HasFlag(rep.EventType, eventsignals.FlagUpdateFraming) ||
		tax.HasFlag(tax.SignalEventType(rep.ContentSignal), eventsignals.FlagUpdateFraming)
}

// scoredCluster is the best Comparison seen so far for one distinct
// ClusterID, kept alongside the candidate Representation it came from so
// selectMemberRole can inspect it without Comparison needing to carry a
// full Representation itself.
type scoredCluster struct {
	comparison     Comparison
	representation eventsignals.Representation
}

// DecideJoin decides what to do with an incoming article, given its event
// Representation and every Candidate a bounded, SQL-filtered lookup (Task
// 7/8) already narrowed down. It is pure and deterministic: now is an
// explicit parameter (used only to stamp Decision.DecidedAt) exactly like
// eventsignals.Extract's now parameter — DecideJoin never reads
// wall-clock time itself.
//
// Candidates are grouped by ClusterID and each cluster's best-scoring
// Comparison represents it; DecideJoin then ranks distinct clusters
// against each other (see Config's threshold doc comments for the
// create/ambiguous/join split, and JoinMargin for the "clearly beats
// alternatives" rule). A candidate with an empty ClusterID is never
// considered — see ADR 0009 for why one can exist (crash recovery) and
// why it must never be joined regardless of score.
func DecideJoin(incoming eventsignals.Representation, candidates []Candidate, cfg Config, now time.Time) Decision {
	bestByCluster := map[string]scoredCluster{}
	var order []string // first-seen order, for a deterministic sort key on score ties

	for _, cand := range candidates {
		if cand.ClusterID == "" {
			continue
		}
		cmp := Compare(incoming, cand, cfg)
		existing, ok := bestByCluster[cand.ClusterID]
		if !ok {
			order = append(order, cand.ClusterID)
			bestByCluster[cand.ClusterID] = scoredCluster{comparison: cmp, representation: cand.Representation}
			continue
		}
		if cmp.Score > existing.comparison.Score {
			bestByCluster[cand.ClusterID] = scoredCluster{comparison: cmp, representation: cand.Representation}
		}
	}

	if len(order) == 0 {
		return Decision{Action: ActionCreate, RulesVersion: cfg.RulesVersion, DecidedAt: now}
	}

	sort.SliceStable(order, func(i, j int) bool {
		return bestByCluster[order[i]].comparison.Score > bestByCluster[order[j]].comparison.Score
	})

	best := bestByCluster[order[0]]
	secondScore := -1.0
	var alternatives []Alternative
	for _, clusterID := range order[1:] {
		sc := bestByCluster[clusterID]
		alternatives = append(alternatives, Alternative{
			ClusterID: sc.comparison.ClusterID,
			ArticleID: sc.comparison.ArticleID,
			Score:     sc.comparison.Score,
			Relation:  sc.comparison.Relation,
		})
	}
	if len(order) > 1 {
		secondScore = bestByCluster[order[1]].comparison.Score
	}

	decision := Decision{
		Score:        best.comparison.Score,
		Evidence:     best.comparison.Evidence,
		Alternatives: alternatives,
		RulesVersion: cfg.RulesVersion,
		DecidedAt:    now,
	}

	switch {
	case best.comparison.Score < cfg.AmbiguousLow:
		decision.Action = ActionCreate
	case best.comparison.Score < cfg.AmbiguousHigh:
		decision.Action = ActionAmbiguous
	case best.comparison.Score < cfg.JoinThreshold:
		decision.Action = ActionAmbiguous
	case secondScore >= 0 && best.comparison.Score-secondScore < cfg.JoinMargin:
		decision.Action = ActionAmbiguous
	default:
		decision.Action = ActionJoin
		decision.ClusterID = best.comparison.ClusterID
		decision.MemberRole = selectMemberRole(incoming, best.representation, best.comparison.Relation, cfg.Taxonomy)
	}

	return decision
}

// selectMemberRole picks the MemberRole for a join, from the incoming
// article's own framing first (correction/confirmation/analysis/reaction
// are explicit stances an article takes, so they take precedence),
// falling back to the winning Comparison's dedup relation, then to
// whether the incoming article's Action progressed the event, and
// finally to MemberRoleConfirmation as the default for "another
// independent report of the same fact" — corroboration is itself a form
// of confirmation even without an explicit "confirmed" cue.
func selectMemberRole(incoming, candRep eventsignals.Representation, relation Relation, tax eventsignals.Taxonomy) MemberRole {
	if role, ok := framingMemberRole(incoming, tax); ok {
		return role
	}
	switch {
	case relation == RelationExactDuplicate || relation == RelationNearCopy:
		return MemberRoleDuplicate
	case incoming.Action != "" && candRep.Action != "" && incoming.Action != candRep.Action:
		return MemberRoleSubstantialUpdate
	default:
		return MemberRoleConfirmation
	}
}

// framingRolePrecedence is the order selectMemberRole checks the incoming
// article's framing roles in when its EventType and its ContentSignal's
// EventType name different ones.
var framingRolePrecedence = []MemberRole{
	MemberRoleCorrection, MemberRoleConfirmation, MemberRoleAnalysis, MemberRoleReaction,
}

// framingMemberRole returns the first role in framingRolePrecedence that
// tax assigns to incoming's EventType or to the EventType its
// ContentSignal maps to.
func framingMemberRole(incoming eventsignals.Representation, tax eventsignals.Taxonomy) (MemberRole, bool) {
	typeRole := MemberRole(tax.MemberRole(incoming.EventType))
	signalRole := MemberRole(tax.MemberRole(tax.SignalEventType(incoming.ContentSignal)))
	for _, role := range framingRolePrecedence {
		if typeRole == role || signalRole == role {
			return role, true
		}
	}
	return "", false
}
