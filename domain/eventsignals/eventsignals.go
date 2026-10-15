// Package eventsignals extracts a structured "event representation" from
// an article's title and summary, plus body-aware entity matching: what
// happened (event type, action), who it involves (entities, primary team,
// opponent), when (event date and window), and why (evidence). Entity
// matching additionally reads Article.Body (at lower confidence than
// title/description); every other signal reads only Article.Title and
// Article.Summary. It never reaches into infrastructure, application, or
// adapter code: the
// vocabulary it matches against (Rules) is a plain data struct populated
// by a caller, most likely a later YAML loader.
//
// Nothing in v2 consumes this package yet. A later clustering task will
// use Representation to decide whether two articles describe the same
// event, and to distinguish a genuine event match from two articles that
// merely happen to share an entity.
package eventsignals

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// EventType is the coarse category Extract assigns to an article's
// underlying event. It is what a later clustering task compares first: two
// articles about the same player but different EventTypes (e.g. an injury
// update and a contract extension) describe different events even though
// they share an entity.
type EventType string

const (
	EventTypeTransaction  EventType = "transaction"
	EventTypeInjury       EventType = "injury"
	EventTypeDraft        EventType = "draft"
	EventTypeGame         EventType = "game"
	EventTypePractice     EventType = "practice"
	EventTypeQuote        EventType = "quote"
	EventTypeRumor        EventType = "rumor"
	EventTypeConfirmation EventType = "confirmation"
	EventTypeCorrection   EventType = "correction"
	EventTypeAnalysis     EventType = "analysis"
	EventTypeReaction     EventType = "reaction"
	EventTypeGeneric      EventType = "generic"
)

// Action is the specific verb Extract detected as describing what
// happened — or, for report/confirm/correct, what the article itself is
// doing to an earlier claim. The zero value ("") is a valid outcome: many
// articles (reactions, generic team news) have no single action verb to
// report.
type Action string

const (
	ActionSign     Action = "sign"
	ActionRelease  Action = "release"
	ActionTrade    Action = "trade"
	ActionWaive    Action = "waive"
	ActionClaim    Action = "claim"
	ActionExtend   Action = "extend"
	ActionInjure   Action = "injure"
	ActionReturn   Action = "return"
	ActionPractice Action = "practice"
	ActionActivate Action = "activate"
	ActionDraft    Action = "draft"
	ActionPreview  Action = "preview"
	ActionResult   Action = "result"
	ActionQuote    Action = "quote"
	ActionReport   Action = "report"
	ActionConfirm  Action = "confirm"
	ActionCorrect  Action = "correct"

	// New in the entity-extraction/fingerprint work: finer
	// granularity than the original 17, so a topic-group mapping (see
	// Rules.TopicGroups) can distinguish e.g. an IR placement (both
	// "injury" and "transaction") from a general injury update.
	ActionInjuredReserve Action = "injured-reserve"
	ActionPracticeSquad  Action = "practice-squad"
	ActionPromote        Action = "promotion"
	ActionDemote         Action = "demotion"
	ActionTryout         Action = "tryout"
	ActionContract       Action = "contract"
	ActionGameStatus     Action = "game-status"
	ActionDepthChart     Action = "depth-chart"
	ActionRoster         Action = "roster"
)

// actionPriority is the order Extract checks Actions in when more than one
// matches the same article: whichever comes first here wins the single
// Action field on Representation. The order follows the plan's Action
// taxonomy so it is easy to audit against the spec; ties are broken in
// favor of the more specific transaction/injury verbs before the more
// generic reporting/framing ones.
var actionPriority = []Action{
	ActionPracticeSquad, ActionDemote,
	ActionSign, ActionRelease, ActionTrade, ActionWaive, ActionClaim, ActionExtend, ActionContract,
	ActionInjuredReserve, ActionInjure, ActionReturn, ActionPractice, ActionActivate,
	ActionPromote, ActionRoster, ActionDepthChart,
	ActionTryout, ActionGameStatus,
	ActionDraft, ActionPreview, ActionResult,
	ActionQuote, ActionReport, ActionConfirm, ActionCorrect,
}

// bodyFallbackActionPriority is intentionally narrower than actionPriority.
// Body text can contain boilerplate, related links, or recaps that should not
// override a clear title/summary signal. Use it only when title+summary found
// no action, and favor concrete roster/injury transactions over generic verbs.
var bodyFallbackActionPriority = []Action{
	ActionInjuredReserve,
	ActionPracticeSquad,
	ActionPromote,
	ActionDemote,
	ActionActivate,
	ActionWaive,
	ActionClaim,
	ActionRelease,
	ActionTrade,
	ActionSign,
	ActionRoster,
	ActionInjure,
}

// ContentSignal is a textual stance or format cue Extract detects
// independently of any single Action verb: whether the article hedges a
// claim, confirms one, corrects one, or is written as analysis, reaction,
// or quote coverage rather than hard news. classifyEventType consults
// ContentSignal ahead of Action, because the same underlying action verb
// ("sign") can describe a rumor, a transaction, or a confirmation
// depending on how the article frames it.
type ContentSignal string

const (
	ContentSignalRumor        ContentSignal = "rumor"
	ContentSignalConfirmation ContentSignal = "confirmation"
	ContentSignalCorrection   ContentSignal = "correction"
	ContentSignalAnalysis     ContentSignal = "analysis"
	ContentSignalReaction     ContentSignal = "reaction"
	ContentSignalQuote        ContentSignal = "quote"
)

// contentSignalPriority mirrors classifyEventType's precedence: a
// correction framing outranks a confirmation framing, which outranks a
// rumor framing, if an article's text somehow matched cues for more than
// one (e.g. a correction that quotes the original, wrong report).
var contentSignalPriority = []ContentSignal{
	ContentSignalCorrection, ContentSignalConfirmation, ContentSignalRumor,
	ContentSignalAnalysis, ContentSignalReaction, ContentSignalQuote,
}

// Evidence is one human-readable, audit-suitable explanation of a signal
// Extract matched — which phrase, for which rule, drove part of a
// Representation. Evidence strings are meant to be logged/persisted
// verbatim, not parsed.
type Evidence string

// EntityMention is one recognized mention of an EntityRule inside an
// article, replacing a bare canonical-name string with enough structure
// for a caller to know where it was found and how confidently. Kind
// EntityKindPlayer/EntityKindCoach/EntityKindExecutive mentions are what
// PrimaryEntities/SecondaryEntities (Task 4) are built from;
// EntityKindTeam/EntityKindCompetition mentions never become primary or
// secondary.
type EntityMention struct {
	ID         string
	Name       string
	Kind       EntityKind
	Field      string // "title" | "description" | "body"
	Confidence float64
	// Scope is empty for the team catalog and ScopeLeague for league
	// players; omitted from JSON when empty so stored representations of
	// Team mentions are unchanged.
	Scope EntityScope `json:",omitempty"`
}

// Representation is the structured event signal Extract derives from one
// article's title and summary, plus body-aware entity matching.
type Representation struct {
	ArticleID   string
	SourceID    string
	Domain      string
	ContentType article.ContentType
	PublishedAt time.Time
	FetchedAt   time.Time

	// Entities is every recognized entity mention, ordered by true
	// first-mention position in the article's text: title mentions before
	// summary mentions before body mentions, then lowest character offset
	// within whichever of those texts the mention was found in (the same
	// rule earliestOfKind uses for PrimaryEntity) — not the order entity
	// rules happen to be declared in Rules.Entities. PrimaryEntity is
	// Extract's best guess at the article's
	// main subject: the earliest-mentioned Player entity among title/
	// description matches only (body-tier matches are never eligible),
	// otherwise the earliest-mentioned Team, otherwise the earliest
	// Competition. Player entities are preferred over Team entities even
	// when the team is mentioned first (e.g. "Isotopes WR X returns...")
	// because PrimaryTeam already carries the team-level signal
	// separately — naming the player as PrimaryEntity is the more useful
	// answer to "who is this story about".
	Entities      []string
	PrimaryEntity string

	// PrimaryEntities is every player/coach/executive EntityMention tied
	// for the earliest position (by rankOf: title before description
	// before body, then lowest offset within that field) among all such
	// mentions — the article's main subject(s). In practice this is
	// almost always a single entity: two mentions only tie when their
	// exact rank matches, which excludes every other player/coach/
	// executive named later in the very same sentence.
	// SecondaryEntities is every other matched player/coach/executive
	// mention. Team/competition mentions are never primary or secondary;
	// see Teams and the existing PrimaryTeam/Opponent fields.
	PrimaryEntities   []string
	SecondaryEntities []string
	// Teams is every matched team EntityMention's ID, in first-mention
	// order. PrimaryTeam/Opponent (existing fields) remain the
	// canonical-name view clustering.Compare already reads.
	Teams []string

	// EntityMentions is every recognized entity mention, one per matched
	// EntityRule, ordered the same way Entities is (true first-mention
	// position: title before description before body, then lowest offset
	// within that field). Entities (the pre-existing []string field) is
	// kept unchanged for anything reading already-persisted
	// representation_json.
	EntityMentions []EntityMention

	PrimaryTeam string
	Opponent    string
	Competition string

	// EventDate is article.PublishedAt, or now when PublishedAt is zero.
	// article.Validate already requires PublishedAt to be set, so in the
	// current pipeline every article reaching Extract has one; this
	// fallback exists only so Extract never silently produces a zero
	// EventDate if that invariant is ever relaxed upstream.
	EventDate time.Time
	// EventWindow is Rules.EventWindows[EventType]; the span, starting at
	// EventDate, within which a later clustering task expects other
	// coverage of the same event to appear.
	EventWindow time.Duration

	EventType     EventType
	Action        Action
	ContentSignal ContentSignal

	// Topics is derived from Action/EventType via Rules.TopicGroups —
	// zero or more coarse tags, deterministically ordered by Topic name.
	Topics []string
	// Events mirrors Action as a single-element slice (or empty when
	// Action == ""), matching Fingerprint's array shape. Action itself is
	// unchanged and still what clustering.Compare reads.
	Events []string

	// ApproximateKeys are candidate clustering keys, strongest (most
	// specific to this exact event) first and weakest (broadest,
	// same-subject-only) last. A later clustering task uses these to
	// bound its candidate search instead of scanning every article.
	ApproximateKeys []string

	Evidence []Evidence

	// UnknownEntities is every name-like span Extract found but could not
	// match to any EntityRule — evidence for future
	// LLM-fallback resolution and offline catalog maintenance, never
	// consumed by clustering itself.
	UnknownEntities []UnknownEntity
}

// Extract derives a Representation from a using rules' vocabulary. now is
// used only as the EventDate fallback when a.PublishedAt is zero.
func Extract(a article.Article, rules Rules, now time.Time) Representation {
	rep := Representation{
		ArticleID:   a.ID,
		SourceID:    a.Source.ID.String(),
		Domain:      a.Domain,
		ContentType: a.ContentType,
		PublishedAt: a.PublishedAt,
		FetchedAt:   a.FetchedAt,
	}

	rep.EventDate = a.PublishedAt
	if rep.EventDate.IsZero() {
		rep.EventDate = now
	}

	titleText := NormalizeSynonyms(strings.ToLower(canonPunct(a.Title)), rules.Synonyms)
	summaryText := NormalizeSynonyms(strings.ToLower(canonPunct(a.Summary)), rules.Synonyms)
	fullText := strings.TrimSpace(titleText + " " + summaryText)

	action, actionEvidence := detectAction(fullText, rules.ActionTerms)
	signal, signalEvidence := detectContentSignal(fullText, rules.ContentCues)

	bodyText := NormalizeSynonyms(strings.ToLower(canonPunct(a.Body)), rules.Synonyms)
	if action == "" && bodyText != "" {
		action, actionEvidence = detectActionWithPriority(bodyText, rules.ActionTerms, bodyFallbackActionPriority, "body")
	}
	det := detectEntities(titleText, summaryText, bodyText, rules.Entities, rules.FieldWeights)

	rep.Action = action
	rep.ContentSignal = signal
	rep.Entities = det.entities
	rep.EntityMentions = det.mentions
	rep.PrimaryEntity = det.primary
	rep.PrimaryTeam = det.team
	rep.Opponent = det.opponent
	rep.Competition = det.competition
	rep.PrimaryEntities = det.primaryEntities
	rep.SecondaryEntities = det.secondaryEntities
	rep.Teams = det.teams
	rep.EventType = classifyEventType(action, signal)
	rep.EventWindow = rules.EventWindows[rep.EventType]

	rep.Topics = topicsFor(rep.Action, rep.EventType, rules.TopicGroups)
	if rep.Action != "" {
		rep.Events = []string{string(rep.Action)}
	}

	rep.Evidence = append(rep.Evidence, actionEvidence...)
	rep.Evidence = append(rep.Evidence, signalEvidence...)
	rep.Evidence = append(rep.Evidence, det.evidence...)

	rep.UnknownEntities = detectUnknownEntities(a.Title, a.Summary, a.Body, rules.Entities, rules.UnknownEntityStoplist)

	rep.ApproximateKeys = buildApproximateKeys(rep)

	return rep
}

// topicsFor returns every Topic in rules.TopicGroups whose value list
// contains action or eventType (as raw strings), sorted by Topic name for
// deterministic output.
func topicsFor(action Action, eventType EventType, groups map[Topic][]string) []string {
	var topics []string
	for topic, values := range groups {
		for _, v := range values {
			if v == string(action) || v == string(eventType) {
				topics = append(topics, string(topic))
				break
			}
		}
	}
	sort.Strings(topics)
	return topics
}

// classifyEventType picks a single EventType from the detected action and
// content signal. ContentSignal is consulted first: a correction,
// confirmation, or rumor framing describes the article's relationship to
// the underlying event, and takes precedence over which specific verb
// (sign, trade, extend, ...) that event involved. Action-derived buckets
// come next, and format-only ContentSignals (analysis/reaction/quote) are
// the fallback for articles with no specific action verb at all.
func classifyEventType(action Action, signal ContentSignal) EventType {
	switch signal {
	case ContentSignalCorrection:
		return EventTypeCorrection
	case ContentSignalConfirmation:
		return EventTypeConfirmation
	case ContentSignalRumor:
		return EventTypeRumor
	}

	switch action {
	case ActionSign, ActionRelease, ActionTrade, ActionWaive, ActionClaim, ActionExtend, ActionContract,
		ActionPracticeSquad, ActionPromote, ActionDemote, ActionRoster:
		return EventTypeTransaction
	case ActionInjure, ActionInjuredReserve, ActionReturn, ActionActivate, ActionGameStatus:
		return EventTypeInjury
	case ActionPractice, ActionTryout, ActionDepthChart:
		return EventTypePractice
	case ActionDraft:
		return EventTypeDraft
	case ActionPreview, ActionResult:
		return EventTypeGame
	case ActionQuote:
		return EventTypeQuote
	case ActionReport:
		return EventTypeRumor
	case ActionConfirm:
		return EventTypeConfirmation
	case ActionCorrect:
		return EventTypeCorrection
	}

	switch signal {
	case ContentSignalQuote:
		return EventTypeQuote
	case ContentSignalAnalysis:
		return EventTypeAnalysis
	case ContentSignalReaction:
		return EventTypeReaction
	}

	return EventTypeGeneric
}

// detectAction returns the highest-priority Action (see actionPriority)
// whose terms include a phrase found in text, and one Evidence string
// naming the phrase that matched. text is assumed already lower-cased and
// synonym-normalized.
func detectAction(text string, terms map[Action][]string) (Action, []Evidence) {
	return detectActionWithPriority(text, terms, actionPriority, "")
}

func detectActionWithPriority(text string, terms map[Action][]string, priority []Action, field string) (Action, []Evidence) {
	for _, action := range priority {
		for _, term := range terms[action] {
			if matched, _ := matchTerm(text, term); matched {
				if field != "" {
					return action, []Evidence{Evidence(fmt.Sprintf("action %q matched phrase %q in %s", action, term, field))}
				}
				return action, []Evidence{Evidence(fmt.Sprintf("action %q matched phrase %q", action, term))}
			}
		}
	}
	return "", nil
}

// detectContentSignal mirrors detectAction for ContentSignal cues.
func detectContentSignal(text string, cues map[ContentSignal][]string) (ContentSignal, []Evidence) {
	for _, signal := range contentSignalPriority {
		for _, term := range cues[signal] {
			if matched, _ := matchTerm(text, term); matched {
				return signal, []Evidence{Evidence(fmt.Sprintf("content signal %q matched phrase %q", signal, term))}
			}
		}
	}
	return "", nil
}

// entityMatch is one EntityRule found in the article's text, kept only
// long enough to pick a primary entity, build EntityMentions, and build
// evidence.
type entityMatch struct {
	rule  EntityRule
	pos   int
	field string // "title" | "description" | "body"
}

// fieldTier orders the three text fields by editorial weight — title
// carries the most, body the least — independent of Rules.FieldWeights'
// actual numeric values (which are Confidence, not ranking order).
var fieldTier = map[string]int{"title": 0, "description": 1, "body": 2}

// rankOf orders entityMatches by true text position: any earlier-tier
// field (title, then description, then body) outranks any later-tier
// field regardless of character offset, and within the same field lower
// character offsets win. Every position-based selection in this file
// (Entities/EntityMentions order, PrimaryEntity, PrimaryTeam, Opponent,
// Competition, PrimaryEntities/SecondaryEntities) sorts or compares by
// this single rank so they all agree on what "first mentioned" means.
func rankOf(m entityMatch) int {
	return fieldTier[m.field]*(1<<40) + m.pos
}

// entityDetection is detectEntities' full result: every field a caller
// (Extract, Task 4's primary/secondary split) needs, computed once from
// one pass over matches.
type entityDetection struct {
	entities    []string
	mentions    []EntityMention
	primary     string
	team        string
	opponent    string
	competition string
	evidence    []Evidence

	primaryEntities   []string
	secondaryEntities []string
	teams             []string
}

// detectEntities finds every EntityRule with at least one matching term in
// titleText, summaryText, or bodyText (already lower-cased and
// synonym-normalized), and derives every mention, the primary entity, the
// primary team, the opponent, and the competition from them. Evidence
// retains rules-declaration order, matching the order Extract's vocabulary
// is walked; every position-based field (entities, mentions, Opponent,
// Competition, PrimaryEntity) is derived by text position rather than
// rules order (see rankOf).
func detectEntities(titleText, summaryText, bodyText string, rules []EntityRule, fieldWeights map[string]float64) entityDetection {
	var matches, leagueMatches []entityMatch
	var evidence []Evidence
	var words map[string]struct{} // built lazily, only when a rule carries a Prefilter
	for _, rule := range rules {
		if len(rule.Prefilter) > 0 {
			if words == nil {
				words = articleWords(titleText, summaryText, bodyText)
			}
			present := false
			for _, w := range rule.Prefilter {
				if _, ok := words[w]; ok {
					present = true
					break
				}
			}
			if !present {
				continue
			}
		}
		pos, field, term, ok := firstMatch(titleText, summaryText, bodyText, rule.Terms)
		if !ok && len(rule.ConditionalTerms) > 0 {
			pos, field, term, ok = firstConditionalMatch(titleText, summaryText, bodyText, rule.ConditionalTerms)
		}
		if !ok {
			continue
		}
		m := entityMatch{rule: rule, pos: pos, field: field}
		if rule.Scope == ScopeLeague {
			leagueMatches = append(leagueMatches, m) // recorded below, never selected, no Evidence
			continue
		}
		matches = append(matches, m)
		evidence = append(evidence, Evidence(fmt.Sprintf("entity %q matched phrase %q in %s", rule.CanonicalName, term, field)))
	}

	ordered := make([]entityMatch, len(matches))
	copy(ordered, matches)
	sort.SliceStable(ordered, func(i, j int) bool { return rankOf(ordered[i]) < rankOf(ordered[j]) })

	// Entities and EntityMentions list both scopes in true first-mention order.
	// ordered (team scope) stays the only input to every selection below;
	// league matches are merged in here and nowhere else. On a rank tie the
	// team entry comes first.
	listed := make([]entityMatch, 0, len(ordered)+len(leagueMatches))
	listed = append(listed, ordered...)
	listed = append(listed, leagueMatches...)
	sort.SliceStable(listed, func(i, j int) bool { return rankOf(listed[i]) < rankOf(listed[j]) })

	entities := make([]string, len(listed))
	mentions := make([]EntityMention, len(listed))
	for i, m := range listed {
		entities[i] = m.rule.CanonicalName
		mentions[i] = EntityMention{
			ID: m.rule.ID, Name: m.rule.CanonicalName, Kind: m.rule.Kind,
			Field: m.field, Confidence: fieldWeights[m.field], Scope: m.rule.Scope,
		}
	}

	// nonBodyMatches restricts primary-selection eligibility (PrimaryEntity,
	// PrimaryTeam, Opponent, Competition, PrimaryEntities) to title/
	// description tier matches only. Body-tier matches remain fully visible
	// via Entities/EntityMentions/SecondaryEntities — they are simply never
	// eligible to become primary or to drive PrimaryTeam/Opponent/
	// Competition, per the design spec's promise that a body mention can
	// only ever land as a low-confidence secondary. Teams (the ID list used
	// by Fingerprint) is unaffected and keeps all tiers.
	nonBodyMatches := make([]entityMatch, 0, len(matches))
	for _, m := range matches {
		if m.field != "body" {
			nonBodyMatches = append(nonBodyMatches, m)
		}
	}

	team := earliestMatching(nonBodyMatches, func(r EntityRule) bool { return r.Kind == EntityKindTeam && r.IsPrimaryTeam })
	opponent := earliestMatching(nonBodyMatches, func(r EntityRule) bool { return r.Kind == EntityKindTeam && !r.IsPrimaryTeam })
	competition := earliestMatching(nonBodyMatches, func(r EntityRule) bool { return r.Kind == EntityKindCompetition })

	primary := earliestOfKind(nonBodyMatches, EntityKindPlayer)
	if primary == "" {
		primary = earliestOfKind(nonBodyMatches, EntityKindTeam)
	}
	if primary == "" {
		primary = earliestOfKind(nonBodyMatches, EntityKindCompetition)
	}

	isPerson := func(k EntityKind) bool {
		return k == EntityKindPlayer || k == EntityKindCoach || k == EntityKindExecutive
	}

	// bestRank is the rankOf (see rankOf's doc comment: every
	// position-based selection in this file, including
	// PrimaryEntities/SecondaryEntities, compares by this single rank) of
	// the single earliest-mentioned player/coach/executive. Comparing by
	// exact rank rather than field tier alone matters when more than one
	// person is mentioned in the same field (e.g. a title naming both the
	// speaker and the people they discussed): only the true first mention
	// is primary, even though every name in that sentence shares the same
	// tier.
	bestRank := -1
	for _, m := range ordered {
		if !isPerson(m.rule.Kind) || m.field == "body" {
			continue
		}
		if r := rankOf(m); bestRank == -1 || r < bestRank {
			bestRank = r
		}
	}

	var primaryEntities, secondaryEntities, teams []string
	for _, m := range ordered {
		switch {
		case isPerson(m.rule.Kind):
			if bestRank != -1 && m.field != "body" && rankOf(m) == bestRank {
				primaryEntities = append(primaryEntities, m.rule.ID)
			} else {
				secondaryEntities = append(secondaryEntities, m.rule.ID)
			}
		case m.rule.Kind == EntityKindTeam:
			teams = append(teams, m.rule.ID)
		}
	}

	return entityDetection{
		entities: entities, mentions: mentions, primary: primary,
		team: team, opponent: opponent, competition: competition, evidence: evidence,
		primaryEntities: primaryEntities, secondaryEntities: secondaryEntities, teams: teams,
	}
}

// earliestOfKind returns the CanonicalName of the earliest match (by
// rankOf) among matches of the given kind, or "" if none matched.
func earliestOfKind(matches []entityMatch, kind EntityKind) string {
	return earliestMatching(matches, func(r EntityRule) bool { return r.Kind == kind })
}

// earliestMatching returns the CanonicalName of the earliest match (by
// rankOf) among matches whose EntityRule satisfies pred, or "" if none
// matched. It is the shared position-tracking machinery behind
// earliestOfKind (PrimaryEntity) and detectEntities' PrimaryTeam/
// Opponent/Competition selection, so all of them agree on what "first
// mentioned" means.
func earliestMatching(matches []entityMatch, pred func(EntityRule) bool) string {
	best := -1
	bestName := ""
	for _, m := range matches {
		if !pred(m.rule) {
			continue
		}
		rank := rankOf(m)
		if best == -1 || rank < best {
			best = rank
			bestName = m.rule.CanonicalName
		}
	}
	return bestName
}

// articleWords returns the set of lower-case words of the given texts, split
// on every non-word rune (the same tokenisation PrefilterWords uses).
func articleWords(texts ...string) map[string]struct{} {
	words := make(map[string]struct{}, 256)
	for _, t := range texts {
		for _, w := range strings.FieldsFunc(t, func(r rune) bool { return !isWordRune(r) }) {
			words[w] = struct{}{}
		}
	}
	return words
}

// PrefilterWords returns the first word (lower-cased, tokenised like article
// text) of each non-empty term, without duplicates, preserving order.
func PrefilterWords(terms ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range terms {
		fields := strings.FieldsFunc(strings.ToLower(canonPunct(strings.TrimSpace(t))), func(r rune) bool { return !isWordRune(r) })
		if len(fields) == 0 || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		out = append(out, fields[0])
	}
	return out
}

// firstConditionalMatch returns the earliest (by rankOf) conditional term whose
// context is also present in the article.
func firstConditionalMatch(title, summary, body string, cts []ConditionalTerm) (int, string, string, bool) {
	best, bestField, bestTerm, found := 0, "", "", false
	for _, ct := range cts {
		pos, field, term, ok := firstMatch(title, summary, body, []string{ct.Term})
		if !ok {
			continue
		}
		if _, _, _, ctx := firstMatch(title, summary, body, ct.RequireAny); !ctx {
			continue
		}
		if r := rankOf(entityMatch{pos: pos, field: field}); !found || r < rankOf(entityMatch{pos: best, field: bestField}) {
			best, bestField, bestTerm, found = pos, field, term, true
		}
	}
	return best, bestField, bestTerm, found
}

// firstMatch finds the earliest occurrence, across every term in terms,
// checking titleText, then summaryText, then bodyText in that order and
// stopping at the first field with any match. It returns the character
// position of the match, which field it was found in, and which term
// matched.
func firstMatch(titleText, summaryText, bodyText string, terms []string) (pos int, field string, term string, ok bool) {
	if p, t, found := earliestTermMatch(titleText, terms); found {
		return p, "title", t, true
	}
	if p, t, found := earliestTermMatch(summaryText, terms); found {
		return p, "description", t, true
	}
	if p, t, found := earliestTermMatch(bodyText, terms); found {
		return p, "body", t, true
	}
	return 0, "", "", false
}

// earliestTermMatch returns the lowest-position match, across every term
// in terms, found in text.
func earliestTermMatch(text string, terms []string) (pos int, term string, ok bool) {
	best := -1
	bestTerm := ""
	for _, t := range terms {
		if matched, idx := matchTerm(text, t); matched {
			if best == -1 || idx < best {
				best = idx
				bestTerm = t
			}
		}
	}
	if best == -1 {
		return 0, "", false
	}
	return best, bestTerm, true
}

// buildApproximateKeys derives clustering candidate keys from rep,
// strongest (most specific) first. Each key is only added if every
// component it needs is non-empty, and duplicate keys are collapsed. A
// later clustering task uses these to bound its candidate search by exact
// key lookups instead of scanning every stored article.
func buildApproximateKeys(rep Representation) []string {
	day := ""
	if !rep.EventDate.IsZero() {
		day = rep.EventDate.UTC().Format("2006-01-02")
	}

	var keys []string
	seen := map[string]bool{}
	add := func(parts ...string) {
		for _, p := range parts {
			if p == "" {
				return
			}
		}
		slugged := make([]string, len(parts))
		for i, p := range parts {
			slugged[i] = Slug(p)
		}
		key := strings.Join(slugged, ":")
		if seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}

	// Strongest: this exact event, this exact entity/opponent pairing, on
	// this day.
	add(string(rep.EventType), string(rep.Action), rep.PrimaryEntity, rep.Opponent, day)
	add(string(rep.EventType), string(rep.Action), rep.PrimaryEntity, day)
	add(string(rep.EventType), string(rep.Action), rep.PrimaryEntity)
	add(string(rep.EventType), rep.PrimaryEntity)
	// Weakest: same subject, any event type/action/day — a broad catch-all
	// a later task can use to widen its search if nothing stronger hits.
	add(rep.PrimaryEntity)

	return keys
}

// Slug lower-cases s and collapses every run of non-alphanumeric
// characters into a single hyphen, trimming leading/trailing hyphens, so
// slugs are stable, readable, and safe to use as map keys, SQL index
// values, or entity IDs. Exported so a catalog builder (see
// entitycatalogbuild) can derive the same stable ID scheme
// buildApproximateKeys already uses.
func Slug(s string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// matchTerm reports whether term (case-insensitive) appears in
// haystackLower — assumed already lower-cased by the caller — as a whole
// word/phrase, and the byte offset of its first occurrence. A `\b`-style
// boundary is only required on a side where term itself starts/ends with
// a word character (letter, digit, or underscore), mirroring
// dictionary.boundaryPattern: a term like "27-20" does not need a boundary
// before "-" since "-" is already a non-word character no word-boundary
// would match, but "sign" must not match inside "signing" or "resign".
//
// Extract's vocabulary (action terms, content cues, entities) is small —
// tens, not hundreds, of phrases — and Extract runs once per article, not
// in a loop over many texts. Unlike dictionary.Matcher,
// which precompiles regular expressions once via Prepare because it holds
// hundreds of vocabulary entries reused across an entire fetch cycle,
// eventsignals matches with plain string scanning: there is no compiled
// state to amortize, and introducing one here (e.g. a package-level cache
// keyed by Rules.Version) would risk serving stale matches for two
// differently-configured Rules values that both leave Version empty, for
// no measured performance benefit — nothing calls Extract in a hot loop
// yet.
func matchTerm(haystackLower, term string) (bool, int) {
	needle := strings.ToLower(canonPunct(strings.TrimSpace(term)))
	if needle == "" {
		return false, -1
	}
	requireLeft := isWordRune(firstRune(needle))
	requireRight := isWordRune(lastRune(needle))

	searchFrom := 0
	for searchFrom <= len(haystackLower) {
		idx := strings.Index(haystackLower[searchFrom:], needle)
		if idx < 0 {
			return false, -1
		}
		absolute := searchFrom + idx
		end := absolute + len(needle)

		leftOK := !requireLeft || absolute == 0 || !isWordRune(lastRune(haystackLower[:absolute]))
		rightOK := !requireRight || end >= len(haystackLower) || !isWordRune(firstRune(haystackLower[end:]))
		if leftOK && rightOK {
			return true, absolute
		}
		searchFrom = absolute + 1
	}
	return false, -1
}

// replaceTerm replaces every word-boundary-aware occurrence of phrase in
// text (assumed already lower-cased) with canonical.
func replaceTerm(text, phrase, canonical string) string {
	needle := strings.ToLower(canonPunct(strings.TrimSpace(phrase)))
	if needle == "" {
		return text
	}

	var b strings.Builder
	rest := text
	for {
		matched, idx := matchTerm(rest, needle)
		if !matched {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:idx])
		b.WriteString(canonical)
		rest = rest[idx+len(needle):]
	}
	return b.String()
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// Fingerprint is the normalized, clustering-facing view of a
// Representation — the shape clustering consumes. It is a pure
// projection: every field is copied from Representation fields Extract
// already populated. Fingerprint has no consumer yet — a later
// clustering-scoring improvement (see the design's "Follow-up ideas") is
// expected to use it.
type Fingerprint struct {
	PrimaryEntities   []string
	SecondaryEntities []string
	Teams             []string
	Topics            []string
	Events            []string
}

// Fingerprint projects r into its Fingerprint.
func (r Representation) Fingerprint() Fingerprint {
	return Fingerprint{
		PrimaryEntities:   r.PrimaryEntities,
		SecondaryEntities: r.SecondaryEntities,
		Teams:             r.Teams,
		Topics:            r.Topics,
		Events:            r.Events,
	}
}
