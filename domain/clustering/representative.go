package clustering

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// RepresentativeCandidate is one article within an already-formed cluster
// (Action == ActionJoin/ActionCreate has already placed it there) that
// RankRepresentative/ReplacesCurrent scores to decide which article should
// represent the cluster — e.g. which one gets linked in a digest or
// notification. Every candidate has already been assigned to the same
// cluster by the time it reaches this package (Task 7/8's concern, not
// this one's).
type RepresentativeCandidate struct {
	ArticleID string

	// Representation is this candidate's own event signal, produced by an
	// earlier eventsignals.Extract call and persisted alongside it.
	Representation eventsignals.Representation

	// IsPrimarySource is a caller-supplied signal: true when the caller
	// knows — from a per-source configuration table this pure domain
	// package has no access to — that this candidate's source is the
	// primary/official outlet for the event (e.g. the team's own site for
	// a transaction, the league's own newsroom for a ruling).
	//
	// This is deliberately kept separate from RepresentativeConfig's
	// DomainPriorities: IsPrimarySource is a binary "this IS the
	// authoritative voice for this specific event" signal a caller may or
	// may not be able to supply per-event, while DomainPriorities is a
	// static, event-independent credibility score that always applies to
	// every article from that domain. A domain can carry high general
	// priority (e.g. a well-regarded national beat reporter) without ever
	// being the primary/official source for any one event, and vice versa
	// (a lesser-known team PR account IS primary for its own team's news
	// even if its domain has no special reputation otherwise). Zero value
	// (false) means "no opinion" — unlike ClickbaitPenalty this is never
	// inferred from Representation itself.
	IsPrimarySource bool

	// ClickbaitPenalty is a caller-supplied penalty in [0,1] (0 = no
	// clickbait signal, 1 = maximally sensational) that this pure domain
	// package cannot compute itself: headline sensationalism needs the
	// actual article text and a classifier outside eventsignals'
	// title/summary vocabulary matching. See RepresentativeConfig.
	// ClickbaitPenaltyWeight for how strongly it is subtracted from Score.
	// Values outside [0,1] are not rejected here (this package has no
	// validation step, mirroring Candidate.DuplicateScore's "opaque"
	// treatment) but are not meaningful.
	ClickbaitPenalty float64

	// Editorial is the (currently placeholder) editorial-quality score for
	// this candidate, from editorial.Evaluate. Per the plan ("use existing
	// placeholder score without making it a gate"), Editorial contributes
	// only a small, weighted signal — see RepresentativeConfig.
	// EditorialWeight — and, unlike ClickbaitPenalty, can never by itself
	// disqualify or override a candidate's selection.
	Editorial editorial.Score
}

// RepresentativeScore is RankRepresentative/ReplacesCurrent's scored
// verdict on one candidate: how well it serves as its cluster's
// representative, and why.
type RepresentativeScore struct {
	ArticleID string

	// Score is a weighted average, in [0,1], over every applicable signal
	// (see RepresentativeConfig's per-weight doc comments), minus the
	// clickbait penalty deduction — floored at 0, never negative. Unlike
	// Comparison.Score there is no conflict gate that can zero it outright;
	// a large enough ClickbaitPenalty is the only way a candidate's Score
	// collapses to (or toward) 0.
	Score float64

	// Evidence explains Score: one deterministic, human-readable string per
	// signal considered (see scoreCandidate) — stable across repeated calls
	// with identical inputs so golden-style assertions can rely on it.
	Evidence []Evidence

	// RulesVersion is copied from RepresentativeConfig.RulesVersion.
	RulesVersion string
}

// RepresentativeConfig configures RankRepresentative and ReplacesCurrent:
// the weights each ranking signal contributes, the reference data
// (domain priorities, freshness window) needed to score some of those
// signals, and the two thresholds that gate ReplacesCurrent's substantial-
// update decision.
//
// RepresentativeConfig is intentionally a separate type from
// clustering.Config (see Config's own doc comment, which explicitly
// excludes this data): Config scores whether two Representations describe
// the same event; RepresentativeConfig scores which of several
// already-clustered articles best represents that event, an unrelated
// question with unrelated inputs.
type RepresentativeConfig struct {
	// PrimarySourceWeight, DomainPriorityWeight, CompletenessWeight,
	// NoveltyWeight, ClarityWeight, FreshnessWeight, OriginalContentWeight,
	// EditorialWeight, and VerifiabilityWeight are the per-signal weights
	// scoreCandidate uses to build a weighted average in [0,1] — mirroring
	// clustering.Config's "weighted average over only the signals
	// applicable to this pair" idiom exactly, except every signal here is
	// always applicable (none of these needs data from a "second side" the
	// way, e.g., Config.EntityMatchWeight needs both articles to have named
	// an entity) so none is ever excluded from the denominator. All should
	// be >= 0; a weight of 0 disables that signal without needing to
	// remove it from the rules file. Only the ratios between them matter —
	// scoreCandidate always normalizes by their sum.
	//
	//   - PrimarySourceWeight rewards RepresentativeCandidate.
	//     IsPrimarySource: 1.0 when true, 0.0 when false. This is a binary,
	//     caller-supplied "authoritative voice for this event" signal — see
	//     RepresentativeCandidate.IsPrimarySource's doc comment for why it
	//     is kept distinct from domain priority.
	//   - DomainPriorityWeight rewards Representation.Domain's entry in
	//     DomainPriorities (0-100), normalized to [0,1] by dividing by 100.
	//     A domain absent from DomainPriorities uses DefaultDomainPriority
	//     rather than being penalized to 0 — an unrecognized domain is
	//     "unknown", not "untrustworthy".
	//   - CompletenessWeight rewards how many of Representation's core
	//     event fields are populated — PrimaryEntity, Opponent,
	//     Competition, Action, and a non-zero EventDate — as a proxy for
	//     how much the article actually reported. This package never sees
	//     article body text, so field population is the only completeness
	//     signal available; a five-paragraph article and a one-line brief
	//     that both name the same entity/opponent/competition/action score
	//     identically here. See completeness's doc comment for the exact
	//     field list and reasoning.
	//   - NoveltyWeight rewards a candidate contributing information a
	//     baseline Representation lacks — see novelty's doc comment for
	//     exactly which baseline RankRepresentative vs. ReplacesCurrent
	//     each use, and why they differ.
	//   - ClarityWeight rewards a candidate having an identifiable subject
	//     at all: non-empty PrimaryEntity, non-empty Action, and a
	//     non-generic EventType. This is a cheap, explainable proxy for
	//     "this article was clearly about something" — not an NLP
	//     readability or prose-quality model, which this package has no
	//     means to compute.
	//   - FreshnessWeight rewards recency: PublishedAt closer to now scores
	//     higher, linearly decaying to 0 at FreshnessWindow and beyond. See
	//     FreshnessWindow.
	//   - OriginalContentWeight rewards whichever candidate in the set
	//     being scored has the earliest PublishedAt — the presumed
	//     originator of the report, everyone else being presumed to be
	//     relaying/echoing it. This is a cheap proxy, not a plagiarism or
	//     text-reuse detector (this package has no access to article body
	//     text to compare against): being published first is evidence of,
	//     not proof of, original reporting.
	//   - EditorialWeight rewards RepresentativeCandidate.Editorial.Value
	//     (normalized to [0,1] by dividing by 100). Kept small by
	//     convention while editorial.Evaluate still returns
	//     editorial.NeutralScore for every article (Placeholder: true) —
	//     see RepresentativeCandidate.Editorial's doc comment for why this
	//     signal can never gate selection the way ClickbaitPenaltyWeight
	//     can.
	//   - VerifiabilityWeight rewards a candidate naming specific,
	//     checkable facts — Opponent, Competition, PrimaryTeam, and two or
	//     more distinct Entities — halved when ContentSignal is
	//     ContentSignalRumor (a rumor can still name specific parties, but
	//     inherently carries more uncertainty than a hard-news report
	//     naming the same parties). See verifiability's doc comment.
	PrimarySourceWeight   float64
	DomainPriorityWeight  float64
	CompletenessWeight    float64
	NoveltyWeight         float64
	ClarityWeight         float64
	FreshnessWeight       float64
	OriginalContentWeight float64
	EditorialWeight       float64
	VerifiabilityWeight   float64

	// ClickbaitPenaltyWeight scales RepresentativeCandidate.ClickbaitPenalty
	// (already in [0,1]) into a Score deduction: penalty =
	// ClickbaitPenaltyWeight * ClickbaitPenalty, subtracted from the
	// weighted average described above and floored at 0. Unlike every
	// weight above, this is subtracted after the weighted average rather
	// than folded into it — a penalty that merely diluted the average like
	// any other signal could never fully override a high score on every
	// other axis, but the plan requires exactly that ("clickbait penalty
	// can prevent representative selection even from a high-priority
	// domain"). A weight of 0 disables the penalty entirely.
	ClickbaitPenaltyWeight float64

	// DomainPriorities is a 0-100 editorial credibility score keyed by
	// lowercase hostname (e.g. "isotopes.example.com") — the same shape as
	// clusteringrules.RepresentativeRankingConfig.DomainPriorities, so a
	// future loader conversion is a direct field copy with no
	// transformation. A caller should lowercase Representation.Domain
	// before relying on an exact key match if it is not already
	// lowercase; scoreCandidate does the lowercasing itself so callers
	// need not.
	DomainPriorities map[string]float64

	// DefaultDomainPriority (0-100) is used for a Representation.Domain
	// with no entry in DomainPriorities. An unrecognized domain is treated
	// as a caller-chosen neutral/default value rather than being punished
	// as if it were the least trustworthy domain possible.
	DefaultDomainPriority float64

	// FreshnessWindow bounds how far back "recent" extends for the
	// FreshnessWeight signal: an article published FreshnessWindow or more
	// before now scores 0 on freshness; one published at now scores 1,
	// decaying linearly in between. A zero or negative FreshnessWindow
	// disables the freshness signal (treated as inapplicable) rather than
	// dividing by zero.
	FreshnessWindow time.Duration

	// SubstantialUpdateThreshold is the minimum combined novelty+
	// completeness score (the average of the two signals, each in [0,1])
	// for ReplacesCurrent to consider a candidate a genuinely "substantial"
	// update at all — mirroring clustering.Config's AmbiguousLow/
	// JoinThreshold gating idiom. Range (0,1].
	SubstantialUpdateThreshold float64

	// RepresentativeSwitchMargin is the minimum overall Score gap a
	// candidate must hold over the current representative, in addition to
	// clearing SubstantialUpdateThreshold, for ReplacesCurrent to report a
	// replacement — mirroring clustering.Config.JoinMargin's "clearly
	// beats" idiom. Both gates must clear: SubstantialUpdateThreshold alone
	// would let a barely-higher-scoring update displace a much stronger
	// existing representative just because it happened to add one new
	// field: RepresentativeSwitchMargin alone would let a routine,
	// non-substantial re-rank (e.g. a fresher clickbait-free reprint)
	// silently swap the representative on every cluster update. Range
	// [0,1].
	RepresentativeSwitchMargin float64

	// RulesVersion identifies the rule set that produced these weights/
	// thresholds, for audit purposes only — copied verbatim onto every
	// RepresentativeScore.RulesVersion, never interpreted by this package.
	RulesVersion string
}

// completeness is the fraction, in [0,1], of Representation's core event
// fields that are populated: PrimaryEntity, Opponent, Competition, Action,
// and a non-zero EventDate. This package never sees article body text, so
// field population — how much of "who/what/when" the extraction pipeline
// could pin down — is the only completeness proxy available; it does not
// distinguish a thorough five-paragraph report from a one-line brief that
// both happen to populate the same fields.
func completeness(rep eventsignals.Representation) float64 {
	total := 5.0
	got := 0.0
	if rep.PrimaryEntity != "" {
		got++
	}
	if rep.Opponent != "" {
		got++
	}
	if rep.Competition != "" {
		got++
	}
	if rep.Action != "" {
		got++
	}
	if !rep.EventDate.IsZero() {
		got++
	}
	return got / total
}

// clarity is 1 when rep has an identifiable subject at all — non-empty
// PrimaryEntity, non-empty Action, and a non-generic EventType, averaged —
// a cheap, explainable proxy for "this article was clearly about
// something", not an NLP readability model.
func clarity(rep eventsignals.Representation) float64 {
	total := 3.0
	got := 0.0
	if rep.PrimaryEntity != "" {
		got++
	}
	if rep.Action != "" {
		got++
	}
	if rep.EventType != "" && rep.EventType != eventsignals.EventTypeGeneric {
		got++
	}
	return got / total
}

// verifiability is a proxy for "this article commits to checkable facts",
// in [0,1]: the fraction of {Opponent, Competition, PrimaryTeam, two or
// more distinct Entities} present, halved when ContentSignal is
// ContentSignalRumor — a rumor can still name specific parties, but
// inherently carries more uncertainty than a hard-news report naming the
// same parties.
func verifiability(rep eventsignals.Representation) float64 {
	total := 4.0
	got := 0.0
	if rep.Opponent != "" {
		got++
	}
	if rep.Competition != "" {
		got++
	}
	if rep.PrimaryTeam != "" {
		got++
	}
	if distinctEntityCount(rep) >= 2 {
		got++
	}
	v := got / total
	if rep.ContentSignal == eventsignals.ContentSignalRumor {
		v /= 2
	}
	return v
}

// distinctEntityCount counts the entities that may influence a decision: league
// scope mentions are recorded only and never count. Representations stored
// before mentions existed carry just Entities, so those fall back to its length.
func distinctEntityCount(rep eventsignals.Representation) int {
	if len(rep.EntityMentions) == 0 {
		return len(rep.Entities)
	}
	n := 0
	for _, m := range rep.EntityMentions {
		if m.Scope != eventsignals.ScopeLeague {
			n++
		}
	}
	return n
}

// novelty measures, in [0,1], how much of {Opponent, Competition, Action,
// EventType} rep contributes beyond baseline: the fraction of those four
// fields that are non-empty/non-generic on rep AND differ from (or are
// absent on) baseline. novelty is always applicable — when rep IS baseline
// (or is identical to it), novelty is 0, contributing no bonus.
//
// The baseline differs by caller:
//   - RankRepresentative uses the earliest-published candidate in the set
//     being ranked (the presumed first report) as baseline for every
//     candidate, since a whole-cluster ranking has no single designated
//     "current representative" to compare against.
//   - ReplacesCurrent uses the current representative's own Representation
//     as baseline, directly matching the plan's wording ("does this
//     article contribute something the current representative doesn't
//     have").
func novelty(rep, baseline eventsignals.Representation) float64 {
	total := 4.0
	got := 0.0
	if rep.Opponent != "" && rep.Opponent != baseline.Opponent {
		got++
	}
	if rep.Competition != "" && rep.Competition != baseline.Competition {
		got++
	}
	if rep.Action != "" && rep.Action != baseline.Action {
		got++
	}
	if rep.EventType != "" && rep.EventType != eventsignals.EventTypeGeneric && rep.EventType != baseline.EventType {
		got++
	}
	return got / total
}

// freshness scores recency in [0,1]: 1 at now, decaying linearly to 0 at
// window and beyond. A zero/negative window or zero PublishedAt makes
// freshness inapplicable (returns 0, applicable=false) rather than
// dividing by zero or treating an unknown publish time as maximally stale.
func freshness(rep eventsignals.Representation, window time.Duration, now time.Time) (value float64, applicable bool) {
	if window <= 0 || rep.PublishedAt.IsZero() {
		return 0, false
	}
	age := now.Sub(rep.PublishedAt)
	if age < 0 {
		age = 0
	}
	if age >= window {
		return 0, true
	}
	return 1 - float64(age)/float64(window), true
}

// domainPriority looks up rep.Domain (case-insensitively) in priorities,
// normalized from its 0-100 scale to [0,1]; falls back to
// defaultPriority/100 when Domain is unset or has no entry.
func domainPriority(rep eventsignals.Representation, priorities map[string]float64, defaultPriority float64) float64 {
	p := defaultPriority
	if v, ok := priorities[strings.ToLower(rep.Domain)]; ok {
		p = v
	}
	return p / 100
}

// earliestPublished returns the RepresentativeCandidate with the earliest
// Representation.PublishedAt among candidates, breaking ties by ArticleID
// (ascending) for determinism. Panics-free on an empty slice by returning
// the zero value — callers here always pass a non-empty slice.
func earliestPublished(candidates []RepresentativeCandidate) RepresentativeCandidate {
	var best RepresentativeCandidate
	haveBest := false
	for _, c := range candidates {
		switch {
		case !haveBest:
			best, haveBest = c, true
		case c.Representation.PublishedAt.Before(best.Representation.PublishedAt):
			best = c
		case c.Representation.PublishedAt.Equal(best.Representation.PublishedAt) && c.ArticleID < best.ArticleID:
			best = c
		}
	}
	return best
}

// scoreCandidate computes candidate's weighted-average Score (see
// RepresentativeConfig's per-weight doc comments), minus the clickbait
// penalty deduction, and one deterministic Evidence string per signal
// considered. novelty is scored against baseline (see novelty's doc
// comment for who supplies which baseline). isEarliestPublished tells the
// OriginalContentWeight signal whether candidate is the presumed
// originator within the set being scored.
func scoreCandidate(candidate RepresentativeCandidate, baseline eventsignals.Representation, isEarliestPublished bool, cfg RepresentativeConfig, now time.Time) (float64, []Evidence) {
	rep := candidate.Representation

	primarySourceValue := 0.0
	if candidate.IsPrimarySource {
		primarySourceValue = 1.0
	}

	domainPriorityValue := domainPriority(rep, cfg.DomainPriorities, cfg.DefaultDomainPriority)
	completenessValue := completeness(rep)
	noveltyValue := novelty(rep, baseline)
	clarityValue := clarity(rep)
	freshnessValue, freshnessApplicable := freshness(rep, cfg.FreshnessWindow, now)
	originalContentValue := 0.0
	if isEarliestPublished {
		originalContentValue = 1.0
	}
	editorialValue := candidate.Editorial.Value / 100
	verifiabilityValue := verifiability(rep)

	type signal struct {
		weight      float64
		applicable  bool
		value       float64
		description string
	}

	signals := []signal{
		{
			cfg.PrimarySourceWeight, true, primarySourceValue,
			fmt.Sprintf("primary source: %v", candidate.IsPrimarySource),
		},
		{
			cfg.DomainPriorityWeight, true, domainPriorityValue,
			fmt.Sprintf("domain priority: %q scored %.2f/1.00", rep.Domain, domainPriorityValue),
		},
		{
			cfg.CompletenessWeight, true, completenessValue,
			fmt.Sprintf("completeness: %.2f of core event fields populated", completenessValue),
		},
		{
			cfg.NoveltyWeight, true, noveltyValue,
			fmt.Sprintf("novelty: %.2f new detail beyond baseline", noveltyValue),
		},
		{
			cfg.ClarityWeight, true, clarityValue,
			fmt.Sprintf("clarity: %.2f identifiable-subject signals present", clarityValue),
		},
		{
			cfg.FreshnessWeight, freshnessApplicable, freshnessValue,
			fmt.Sprintf("freshness: %.2f (published %s ago, window %s)", freshnessValue, now.Sub(rep.PublishedAt).Round(time.Minute), cfg.FreshnessWindow),
		},
		{
			cfg.OriginalContentWeight, true, originalContentValue,
			fmt.Sprintf("original content: earliest published in set = %v", isEarliestPublished),
		},
		{
			cfg.EditorialWeight, true, editorialValue,
			fmt.Sprintf("editorial: %.0f/100 (placeholder=%v, non-gating)", candidate.Editorial.Value, candidate.Editorial.Placeholder),
		},
		{
			cfg.VerifiabilityWeight, true, verifiabilityValue,
			fmt.Sprintf("verifiability: %.2f specific/checkable-fact signals present", verifiabilityValue),
		},
	}

	var numerator, denominator float64
	var evidence []Evidence
	for _, s := range signals {
		if !s.applicable || s.weight <= 0 {
			continue
		}
		denominator += s.weight
		numerator += s.weight * s.value
		evidence = append(evidence, Evidence(s.description))
	}

	score := 0.0
	if denominator > 0 {
		score = numerator / denominator
	}

	if cfg.ClickbaitPenaltyWeight > 0 {
		penalty := cfg.ClickbaitPenaltyWeight * candidate.ClickbaitPenalty
		score -= penalty
		evidence = append(evidence, Evidence(fmt.Sprintf("clickbait penalty: -%.2f applied (weight %.2f x penalty %.2f)", penalty, cfg.ClickbaitPenaltyWeight, candidate.ClickbaitPenalty)))
	}

	if score < 0 {
		score = 0
	}

	return score, evidence
}

// RankRepresentative scores every candidate in a cluster and returns them
// sorted best-representative-first (ties broken by ArticleID ascending, for
// determinism). Novelty is scored against the earliest-published candidate
// in candidates (the presumed first report) — see novelty's doc comment.
// RankRepresentative is pure: it consults only its arguments, never
// wall-clock time or package state, exactly like clustering.Compare/
// DecideJoin.
func RankRepresentative(candidates []RepresentativeCandidate, cfg RepresentativeConfig, now time.Time) []RepresentativeScore {
	if len(candidates) == 0 {
		return nil
	}

	baseline := earliestPublished(candidates)

	scores := make([]RepresentativeScore, len(candidates))
	for i, c := range candidates {
		isEarliest := c.ArticleID == baseline.ArticleID
		score, evidence := scoreCandidate(c, baseline.Representation, isEarliest, cfg, now)
		scores[i] = RepresentativeScore{
			ArticleID:    c.ArticleID,
			Score:        score,
			Evidence:     evidence,
			RulesVersion: cfg.RulesVersion,
		}
	}

	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].ArticleID < scores[j].ArticleID
	})

	return scores
}

// ReplacesCurrent decides whether candidate should replace current as a
// cluster's representative. Unlike RankRepresentative's whole-cluster
// baseline, novelty here is scored directly against current.Representation
// — the plan's own wording for this specific decision ("does this article
// contribute something the current representative doesn't have").
//
// A replacement requires both:
//   - SubstantialUpdateThreshold: the average of novelty and completeness
//     (each in [0,1]) against current must reach this threshold — i.e.
//     candidate must be a genuinely substantial update, not just a
//     marginally-better-scored article.
//   - RepresentativeSwitchMargin: candidate's overall Score must exceed
//     current's overall Score by at least this margin.
//
// Both gates must clear (see RepresentativeSwitchMargin's doc comment for
// why neither alone is sufficient). ReplacesCurrent returns candidate's
// RepresentativeScore either way, so a caller can log/persist it even when
// no replacement happens.
func ReplacesCurrent(current, candidate RepresentativeCandidate, cfg RepresentativeConfig, now time.Time) (bool, RepresentativeScore) {
	// OriginalContentWeight's "earliest published in this set" proxy is
	// evaluated over just these two candidates, by actual PublishedAt —
	// not assumed in current's favor just because it is the incumbent
	// representative.
	currentIsEarliest := !current.Representation.PublishedAt.After(candidate.Representation.PublishedAt)

	currentScore, _ := scoreCandidate(current, current.Representation, currentIsEarliest, cfg, now)
	candidateScore, candidateEvidence := scoreCandidate(candidate, current.Representation, !currentIsEarliest, cfg, now)

	result := RepresentativeScore{
		ArticleID:    candidate.ArticleID,
		Score:        candidateScore,
		Evidence:     candidateEvidence,
		RulesVersion: cfg.RulesVersion,
	}

	substantialness := (novelty(candidate.Representation, current.Representation) + completeness(candidate.Representation)) / 2
	if substantialness < cfg.SubstantialUpdateThreshold {
		return false, result
	}
	if candidateScore-currentScore < cfg.RepresentativeSwitchMargin {
		return false, result
	}
	return true, result
}
