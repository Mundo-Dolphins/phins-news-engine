// Package clustering decides whether an incoming article's event
// representation (see eventsignals.Representation) describes the same
// real-world event as an already-clustered article, a different event
// that merely shares an entity, or something too close to call
// automatically. It compares weighted event signals — not raw text — so
// its decisions are explainable independent of wording: two articles
// covering the same signing in different prose still join, and two
// articles about the same player's unrelated injury and contract news
// still do not.
//
// clustering is not a duplicate detector. Near-copy/exact-duplicate text
// similarity is deduplication's domain entirely (see
// domain/deduplication and ADR 0009); this package only ever
// surfaces a candidate's dedup relation as evidence (via
// Candidate.DuplicateRelation) and never rejects an article on the
// strength of it. clustering's only outcomes are ActionJoin, ActionCreate,
// and ActionAmbiguous.
package clustering

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// Status is whether a cluster is still accepting new members.
type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

// Action is what DecideJoin concluded should happen with the incoming
// article.
type Action string

const (
	// ActionJoin means the incoming article belongs in an existing
	// cluster (Decision.ClusterID).
	ActionJoin Action = "join"
	// ActionCreate means no existing cluster scored high enough to even
	// be a candidate; the caller should start a new cluster.
	ActionCreate Action = "create"
	// ActionAmbiguous means the signal was not clear enough to safely
	// join or create automatically — the plan requires this be persisted
	// for later human/editorial review, never silently dropped or
	// rejected. Per the plan's global constraint, a caller facing
	// ActionAmbiguous still creates a new open cluster to preserve the
	// one-cluster-per-article invariant; clustering itself does not do
	// that — it only reports the ambiguity and its evidence.
	ActionAmbiguous Action = "ambiguous"
)

// Relation summarizes how an incoming article's Representation relates to
// one candidate's Representation, from a near-identical restatement down
// to an entirely different event. Relation is diagnostic, not a decision:
// RelationExactDuplicate and RelationNearCopy are surfaced purely as
// dedup-adjacent evidence (see Candidate.DuplicateRelation) — clustering
// never rejects an article because of them, that authority belongs
// entirely to the deduplication package.
type Relation string

const (
	// RelationExactDuplicate: the candidate is, as far as a
	// caller-supplied dedup signal indicates, the same article content
	// restated. Never produced by this package's own scoring — only set
	// on a Comparison when Candidate.DuplicateRelation says so.
	RelationExactDuplicate Relation = "exact_duplicate"
	// RelationNearCopy: the candidate is a near-identical restatement of
	// the same report, short of exact. Like RelationExactDuplicate, only
	// ever set from Candidate.DuplicateRelation, never inferred here.
	RelationNearCopy Relation = "near_copy"
	// RelationSameEvent: the candidate reports the same underlying event
	// (matching entity, action/event-type, date window) without being a
	// copy or contributing new information — the ordinary "join this
	// cluster" case.
	RelationSameEvent Relation = "same_event"
	// RelationUpdate: the candidate is the same event, but the incoming
	// article contributes new information — a confirmation, correction,
	// or a genuine action progression (e.g. "expected to sign" ->
	// "signs") — rather than pure repetition.
	RelationUpdate Relation = "update"
	// RelationDifferent: the candidate does not describe the same event —
	// entity, action/event-type, date window, or opponent conflict. See
	// Compare's conflict gate.
	RelationDifferent Relation = "different"
)

// MemberRole is the part an article plays within the cluster it belongs
// to. DecideJoin assigns one whenever it returns ActionJoin
// (Decision.MemberRole); the roles below that describe how a *new*
// cluster's founding article or a later representative-ranking pass
// (Task 6) relate to a cluster are documented on the constants themselves.
type MemberRole string

const (
	// MemberRoleFirstReport is the role of whichever article starts a
	// cluster (Action == ActionCreate). DecideJoin never assigns this
	// role itself — there is exactly one possible role for a cluster's
	// founding member, so the caller that acts on ActionCreate (Task 8)
	// can assign it directly without needing decision logic here. It
	// exists as a constant so the full MemberRole vocabulary used across
	// persistence and audit is defined in one place.
	MemberRoleFirstReport MemberRole = "first_report"
	// MemberRoleDuplicate marks a join whose winning Comparison carried a
	// RelationExactDuplicate/RelationNearCopy dedup hint (see
	// Candidate.DuplicateRelation) — i.e. the article reached clustering
	// (deduplication did not reject it, e.g. via a content-type
	// exception per ADR 0009) but is still textually a near-copy of an
	// existing member.
	MemberRoleDuplicate MemberRole = "duplicate"
	// MemberRoleConfirmation marks a join where the incoming article
	// either explicitly frames itself as confirming an earlier report
	// (EventType/ContentSignal == confirmation) or, absent any other
	// distinguishing signal, independently reports the same fact as an
	// existing member — a second source is itself a form of
	// confirmation. This is DecideJoin's default join role.
	MemberRoleConfirmation MemberRole = "confirmation"
	// MemberRoleSubstantialUpdate marks a join where the incoming
	// article's Action differs from the winning candidate's Action while
	// every other applicable signal still agrees — a genuine development
	// on the same event (e.g. "expected to sign" -> "signs") rather than
	// a repeat.
	MemberRoleSubstantialUpdate MemberRole = "substantial_update"
	// MemberRoleCorrection marks a join where the incoming article frames
	// itself as correcting an earlier report (EventType/ContentSignal ==
	// correction).
	MemberRoleCorrection MemberRole = "correction"
	// MemberRoleAnalysis marks a join where the incoming article is
	// analysis/commentary on the event rather than new reporting on it
	// (EventType/ContentSignal == analysis).
	MemberRoleAnalysis MemberRole = "analysis"
	// MemberRoleReaction marks a join where the incoming article is
	// reaction coverage of the event (EventType/ContentSignal ==
	// reaction).
	MemberRoleReaction MemberRole = "reaction"
	// MemberRolePrimarySource marks a member as the primary/official
	// source for its cluster. DecideJoin never assigns this role: telling
	// a primary source from an aggregator needs a per-domain trust
	// signal this pure domain package has no access to (that data lives
	// in Task 6's representative-ranking config, which Config
	// deliberately does not duplicate — see Config's doc comment). It
	// exists here so the MemberRole vocabulary Task 6/Task 7 persist
	// against is fully defined in this one package.
	MemberRolePrimarySource MemberRole = "primary_source"
	// MemberRoleRelatedCoverage marks an article joined to a cluster because
	// its wording overlaps with a member's (a rewrite of the same story, or
	// another outlet's take), not because event signals matched. It is another
	// point of view on the story, not a duplicate of it: like analysis and
	// reaction it is exempt from the one-initial-publication gate, and how
	// many such articles reach the channel is bounded by the story guard.
	MemberRoleRelatedCoverage MemberRole = "related_coverage"
)

// Candidate is one existing article — and the cluster it already belongs
// to — that DecideJoin compares the incoming article's Representation
// against. Every candidate has already been assigned to a cluster
// (ClusterID is never empty for a candidate DecideJoin will consider; see
// ADR 0009) by the time it reaches this package. Task 7 is expected to be
// the first caller that actually loads Representation from SQLite; for
// now candidates simply carry whatever Representation the caller supplies
// — tests construct these directly.
type Candidate struct {
	ArticleID string
	ClusterID string

	// Representation is this candidate article's own event signal,
	// produced by an earlier eventsignals.Extract call and persisted
	// alongside it (Task 7).
	Representation eventsignals.Representation

	// DuplicateRelation and DuplicateScore are optional dedup hints,
	// populated by the caller from deduplication.Decide's already-computed
	// score for this same candidate (ADR 0009: deduplication.Decide and
	// clustering.DecideJoin are meant to consume one shared
	// candidate/score computation, never recomputed here). Only
	// RelationExactDuplicate and RelationNearCopy are meaningful values;
	// leave DuplicateRelation "" when the caller has no dedup opinion for
	// this candidate (e.g. it never reached deduplication.Decide, or
	// scored below dedup's own thresholds) — Compare then falls back to
	// scoring the candidate purely on event signals. DuplicateScore is
	// opaque here: Compare never thresholds it, only cites it verbatim in
	// evidence text when DuplicateRelation is set.
	DuplicateRelation Relation
	DuplicateScore    float64
}

// Comparison is the scored comparison between the incoming article's
// Representation and one Candidate's Representation: which weighted
// signals matched, the resulting Score, the inferred Relation, and the
// human-readable Evidence backing it. Compare produces one Comparison per
// candidate; DecideJoin keeps the best Comparison per distinct cluster to
// rank clusters against each other.
type Comparison struct {
	ArticleID string
	ClusterID string

	// Relation is Compare's classification of how the two representations
	// relate; see the Relation constants.
	Relation Relation

	// Score is a weighted average, in [0,1], over every signal below that
	// was applicable to this pair (see Config's per-weight doc comments
	// for exactly when each is applicable) — or exactly 0 when Compare's
	// conflict gate disqualified the pair outright (see Compare).
	Score float64

	// EntityMatch, ActionMatch, EventTypeMatch, OpponentMatch, SourceMatch,
	// and NoveltyMatch are the individual signal outcomes Score was
	// computed from, exposed for audit/testing rather than requiring a
	// caller to recompute them from Score alone. DateWindowMatch is the one
	// exception: Compare's conflict gate (see Config's DateWindowMatchWeight
	// doc comment) already disqualifies any pair whose dates fall outside
	// the window, so DateWindowMatch is always true on any Comparison that
	// reaches this point — it never feeds into Score and is exposed purely
	// for evidence/audit completeness.
	EntityMatch     bool
	ActionMatch     bool
	EventTypeMatch  bool
	OpponentMatch   bool
	DateWindowMatch bool
	SourceMatch     bool
	NoveltyMatch    bool

	// Evidence explains Score and Relation: one string per signal that
	// matched (or, when the conflict gate fired, one string naming the
	// conflict and nothing else).
	Evidence []Evidence
}

// Evidence is one human-readable, audit-suitable explanation of a signal
// that contributed to a Comparison's score or a Decision's outcome.
// Evidence strings mirror eventsignals.Evidence: meant to be logged or
// persisted verbatim, not parsed.
type Evidence string

// Alternative is one candidate cluster DecideJoin considered but did not
// choose, kept on Decision for audit: which cluster (and its
// best-matching member article) came close, its score, and its relation.
type Alternative struct {
	ClusterID string
	ArticleID string
	Score     float64
	Relation  Relation
}

// Decision is the outcome of DecideJoin: what to do with the incoming
// article, which cluster and role if joining, the winning score and
// evidence, and the other top clusters considered — a fully auditable
// trail per the plan's global constraint ("score, evidence, chosen
// cluster, alternatives, rule version, and timestamp").
type Decision struct {
	Action Action

	// ClusterID and MemberRole are set only when Action == ActionJoin.
	ClusterID  string
	MemberRole MemberRole

	// Score is the winning (highest-scoring distinct-cluster) Comparison's
	// score, whatever the Action — including on ActionCreate/
	// ActionAmbiguous, where it explains *why* nothing was joined (e.g.
	// "we created a new cluster even though article X scored 0.30 against
	// it").
	Score float64

	// Evidence explains Score/Action: the winning Comparison's evidence.
	Evidence []Evidence

	// Alternatives lists every other distinct cluster considered besides
	// the winning one, best-scoring first, for audit. Empty when at most
	// one distinct cluster was compared.
	Alternatives []Alternative

	// RulesVersion is copied from Config.RulesVersion. DecidedAt is the
	// now value the caller passed to DecideJoin. Both are carried through
	// rather than looked up here: DecideJoin has no wall-clock access (it
	// is pure and deterministic — see DecideJoin's doc comment).
	RulesVersion string
	DecidedAt    time.Time
}

// Config configures DecideJoin: the thresholds that separate join/
// ambiguous/create outcomes, and the weights Compare's per-candidate
// scoring applies to each signal. Every field's doc comment below is
// written for a YAML-loader author (Task 3) who has not read this
// package's implementation: populate each field exactly as described, in
// the given units, with no additional transformation.
type Config struct {
	// JoinThreshold is the minimum Comparison score, inclusive, for
	// DecideJoin to join the best-scoring candidate's cluster — provided
	// it also clears JoinMargin over the second-best distinct cluster
	// considered (see JoinMargin). Range [0,1].
	JoinThreshold float64

	// JoinMargin is the minimum score gap the best-scoring cluster must
	// hold over the second-best distinct cluster considered, for
	// DecideJoin to join rather than report ActionAmbiguous. This is how
	// "clearly beats alternatives" (the plan's join requirement) is
	// enforced: two candidate clusters scoring within JoinMargin of each
	// other, even both above JoinThreshold, are too close to call
	// automatically and are reported ActionAmbiguous instead. A single
	// qualifying candidate cluster (nothing else to compare it against)
	// always clears this check. Range [0,1].
	JoinMargin float64

	// AmbiguousLow is the minimum Comparison score, inclusive, for
	// DecideJoin to consider a candidate cluster at all; below it,
	// DecideJoin reports ActionCreate (the plan's "create when no
	// candidate reaches ambiguous_low"). Range [0,1].
	AmbiguousLow float64

	// AmbiguousHigh is the minimum Comparison score, inclusive, for
	// DecideJoin to consider a candidate cluster strong enough to
	// evaluate for an actual join at all (still subject to JoinThreshold
	// and JoinMargin) rather than reporting ActionAmbiguous outright.
	// Config is only meaningful when AmbiguousLow <= AmbiguousHigh <=
	// JoinThreshold; this package does not validate that ordering itself
	// (Task 3's loader is expected to). Given that ordering: scores in
	// [AmbiguousLow, AmbiguousHigh) are always ActionAmbiguous; scores in
	// [AmbiguousHigh, JoinThreshold) are also ActionAmbiguous (promising,
	// but short of JoinThreshold); scores >= JoinThreshold still fall
	// back to ActionAmbiguous if JoinMargin over the runner-up is not
	// met. Range [0,1].
	AmbiguousHigh float64

	// EntityMatchWeight, ActionMatchWeight, EventTypeMatchWeight,
	// OpponentMatchWeight, SourcePriorityWeight, and NoveltyMatchWeight are
	// the per-signal weights Compare uses to build Comparison.Score: a
	// weighted average over only the signals applicable to the pair being
	// compared (a signal with no data on either side, e.g. Opponent on two
	// articles that never named one, is excluded from both the numerator
	// and the denominator rather than counted as a non-match). All six
	// should be >= 0; a weight of 0 disables that signal without needing to
	// remove it from the rules file. There is no fixed upper bound on any
	// weight — only the ratios between them matter, since Compare always
	// normalizes by the sum of applicable weights actually used.
	// DateWindowMatchWeight is a seventh, related field documented
	// separately below because — unlike these six — it never contributes to
	// Score at all.
	//
	//   - EntityMatchWeight rewards the incoming article and the
	//     candidate sharing the same Representation.PrimaryEntity.
	//     Applicable only when both sides have a non-empty PrimaryEntity
	//     (like every other two-sided signal here, a signal with no data
	//     on one side is excluded from scoring, not counted as a
	//     non-match). A non-empty, *differing* PrimaryEntity on both
	//     sides is not merely "unmatched" — it disqualifies the candidate
	//     entirely before any weight is applied (see Compare's conflict
	//     gate): two articles about two different primary subjects are
	//     never the same event.
	//   - ActionMatchWeight rewards an identical, non-empty
	//     Representation.Action on both sides. Applicable only when both
	//     sides have a non-empty Action.
	//   - EventTypeMatchWeight rewards an identical
	//     Representation.EventType, OR either side's EventType being one
	//     of confirmation/correction/rumor/analysis/reaction/quote — a
	//     "referential" framing that, by construction
	//     (eventsignals.classifyEventType prioritizes ContentSignal-
	//     derived framing over the action-derived category), describes an
	//     earlier/different-EventType event rather than being its own
	//     event category. Without this carve-out a rumor followed by its
	//     own confirmation, or a transaction followed by its correction,
	//     would never score an EventType match even though they are
	//     definitionally the same event. Always applicable.
	//   - OpponentMatchWeight rewards an identical, non-empty
	//     Representation.Opponent on both sides. Two different, non-empty
	//     opponents disqualify the candidate entirely before any weight
	//     is applied (see Compare's conflict gate), so by the time this
	//     weight is scored the only possibilities are "both named the
	//     same opponent" (matched) or "at least one side named none"
	//     (inapplicable). Applicable only when both sides named an
	//     opponent.
	//   - SourcePriorityWeight rewards the incoming article and the
	//     candidate coming from different, non-empty
	//     Representation.SourceID values — independent corroboration
	//     from a second outlet is stronger evidence of a shared
	//     real-world event than same-source resubmission. This is a
	//     same-vs-different-source signal only; it does not consult any
	//     per-domain trust/priority table (no such data is available to
	//     this pure domain package). A real per-domain priority lookup,
	//     if wanted, belongs in Task 6's representative-ranking config,
	//     which is a separate concern from this join decision. Applicable
	//     only when both sides have a non-empty SourceID.
	//   - NoveltyMatchWeight rewards the incoming article's Action
	//     differing from the candidate's Action while every other
	//     applicable signal still agrees — i.e. the incoming article
	//     reports a genuine development on the same event (an update)
	//     rather than restating the same fact. Applicable only when both
	//     sides have a non-empty Action.
	//
	// DateWindowMatchWeight does NOT participate in Comparison.Score's
	// weighted average the way the six weights above do. Falling outside
	// max(incoming, candidate).EventWindow of the candidate's EventDate
	// disqualifies the candidate entirely before any weight is applied (see
	// Compare's conflict gate), so by the time Compare scores a surviving
	// pair the date window has, structurally, always matched — there is no
	// surviving Comparison where this signal could ever discriminate
	// between candidates. Weighting a signal that can never *not* match
	// would only dilute the six signals above that can, so Compare excludes
	// it from the numerator/denominator entirely (see Comparison.
	// DateWindowMatch). This field is kept only as an evidence on/off
	// toggle: set it > 0 to have Compare cite the date window match in
	// Comparison.Evidence for audit purposes (matching every other
	// weight's "0 disables this signal" convention, even though here the
	// "signal" is evidence-only), or leave it 0 to omit that evidence line.
	// If neither side configures an EventWindow (both zero), no temporal
	// constraint is enforced by the gate either, and the date window is
	// reported as matching by definition.
	EntityMatchWeight     float64
	ActionMatchWeight     float64
	EventTypeMatchWeight  float64
	OpponentMatchWeight   float64
	DateWindowMatchWeight float64
	SourcePriorityWeight  float64
	NoveltyMatchWeight    float64

	// RulesVersion identifies the rule set that produced these thresholds
	// and weights, for audit purposes only — copied verbatim onto every
	// Decision.RulesVersion DecideJoin produces from this Config, never
	// interpreted by this package. Left "" until Task 3's YAML loader
	// populates it; eventsignals.Rules.Version is the analogous field on
	// the eventsignals side.
	RulesVersion string
}
