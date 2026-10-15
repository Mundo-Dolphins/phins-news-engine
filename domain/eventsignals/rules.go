package eventsignals

import (
	"sort"
	"strings"
	"time"
)

// Synonym maps a phrase pattern, as it might literally appear in a raw
// title or summary, to a canonical token. NormalizeSynonyms rewrites every
// occurrence of Phrase to Canonical before any action, content-signal, or
// entity matching runs, so differently-worded coverage of the same idea
// ("agreed to terms", "agrees to a deal") is compared using one normalized
// vocabulary instead of needing every rule list to spell out every surface
// form. This generalizes the phrase -> canonical-token shape found in v1's
// topic_synonyms.yml; the specific v1 phrase list is not reused here.
type Synonym struct {
	Phrase    string
	Canonical string
}

// EntityKind classifies what kind of thing an EntityRule identifies, so
// Extract can tell "the team this bot covers" from an opponent, and either
// from a player or a competition/event context (e.g. "NFL Draft").
type EntityKind string

const (
	EntityKindPlayer      EntityKind = "player"
	EntityKindCoach       EntityKind = "coach"
	EntityKindExecutive   EntityKind = "executive"
	EntityKindTeam        EntityKind = "team"
	EntityKindCompetition EntityKind = "competition"
)

// EntityRule is one recognizable entity: a stable ID, a canonical display
// name, and every surface form Extract should treat as a mention of it.
// Terms are matched against text after synonym normalization,
// case-insensitively, with word-boundary matching (see matchTerm) so
// "dale" does not also match inside "hillside". IsPrimaryTeam marks the
// team this bot covers (e.g. the Springfield Isotopes); it is meaningless on
// non-Team entities. Position is informational only (e.g. "OT", "QB") —
// it is never matched against text as its own entity kind.
// EntityScope says whose roster an EntityRule belongs to. The zero value is
// the team catalog (the only scope that existed before the league
// gazetteer); ScopeLeague marks players of the whole league.
type EntityScope string

// ScopeLeague entities are recorded in Entities/EntityMentions only. They are
// never eligible for PrimaryEntity, PrimaryEntities, SecondaryEntities, Teams,
// PrimaryTeam, Opponent or Competition, and they add no Evidence, so they
// cannot change any clustering decision.
const ScopeLeague EntityScope = "league"

// ConditionalTerm is an alias that only counts when the same article also
// mentions one of RequireAny (for example a surname that needs its team's
// nickname).
type ConditionalTerm struct {
	Term       string
	RequireAny []string
}

type EntityRule struct {
	ID            string
	CanonicalName string
	Kind          EntityKind
	Terms         []string
	IsPrimaryTeam bool
	Position      string
	// TeamID optionally references the EntityKindTeam entity this entity
	// belongs to. Empty means unspecified — see AffiliationStatus for
	// telling a free agent from an unknown affiliation.
	TeamID string
	// AffiliationStatus explicitly declares team / free_agent / unknown.
	// Empty is legacy data (see EffectiveAffiliation).
	AffiliationStatus AffiliationStatus
	// Role is an optional free-form role label (e.g. "starter").
	Role string
	// Scope defaults to the team catalog; see ScopeLeague.
	Scope EntityScope
	// ConditionalTerms are aliases that need extra context in the article.
	ConditionalTerms []ConditionalTerm
	// Prefilter, when non-empty, lists lower-case words: the rule is only
	// evaluated when one of them appears as a word in the article. It is a
	// pure optimisation derived with PrefilterWords from Terms and
	// ConditionalTerms and must never change a result.
	Prefilter []string
}

// Topic is a coarse, cross-cutting tag (e.g. "injury", "transaction",
// "press-conference") distinct from the specific Action/EventType that
// triggered it. Unlike Action/ContentSignal, Topics are never matched
// against text directly — they're derived from whichever Action/EventType
// Extract already detected, via Rules.TopicGroups.
type Topic string

// Rules is the full, versioned vocabulary Extract needs to turn an
// article's title and summary into a Representation: synonym
// normalization, the phrases that signal each Action and each
// ContentSignal, the entities Extract can recognize, and how wide an event
// window to attach per EventType. It is a plain data struct on purpose —
// no compiled regular expressions, no YAML struct tags, no behavior beyond
// NormalizeSynonyms — so a later infrastructure loader (Task 3) can
// populate every field directly from a parsed YAML file without this
// shape needing to change.
type Rules struct {
	// Version identifies the rule set for audit/evidence purposes; it is
	// not interpreted by this package.
	Version string

	Synonyms []Synonym

	// ActionTerms lists, per Action, the phrases (post-synonym) that
	// indicate it. A single Action may be reachable through several
	// phrases (e.g. "sign", "signs", "signed").
	ActionTerms map[Action][]string

	// ContentCues lists, per ContentSignal, the phrases (post-synonym)
	// that indicate that stance or format regardless of which specific
	// Action verb, if any, is also present.
	ContentCues map[ContentSignal][]string

	Entities []EntityRule

	// FieldWeights is Extract's confidence for an entity mention found in
	// each field, and which field "wins" when the same entity is
	// mentioned in more than one (title beats description beats body).
	// Required keys: "title", "description", "body". All three must be
	// present and > 0 (validated by the infrastructure loader, not this
	// package).
	FieldWeights map[string]float64

	// EventWindows is the span of time, from EventDate, within which
	// other coverage of the same event is expected. It has no effect on
	// classification; Extract only looks it up and attaches it to the
	// Representation for a later clustering task to use.
	EventWindows map[EventType]time.Duration

	// TopicGroups maps a Topic to every Action or EventType value (as a
	// raw string) that implies it. A single Action/EventType may imply
	// more than one Topic (e.g. "injured-reserve" implies both "injury"
	// and "transaction"). Extract looks up the detected Action and
	// EventType directly against this map's values.
	TopicGroups map[Topic][]string

	// UnknownEntityStoplist is a set of name-like phrases (case-insensitive)
	// detectUnknownEntities never reports even when they match no
	// EntityRule — days, months, division/conference names, and similar
	// generic Title-Case phrases that would otherwise show up as noisy
	// "unknown entity" evidence.
	UnknownEntityStoplist []string

	// InjuryTypes is a reserved, structurally-validated-but-unconsumed
	// dictionary of injury type/body-part aliases (e.g. "knee", "acl",
	// "concussion"). No code in this package or clustering reads it yet —
	// see configs/event_clustering_rules.yml's domain_priorities/
	// content_type_rules for the established precedent of carrying
	// reserved config data ahead of its consumer.
	InjuryTypes map[string][]string
}

// NormalizeSynonyms rewrites every occurrence of a synonym's Phrase in
// text to its Canonical token, case-insensitively and word-boundary-aware
// (via matchTerm/replaceTerm), before any action/content-signal/entity
// classification runs. text is assumed already lower-cased by the caller.
// Synonyms are applied longest-phrase-first so that a short phrase already
// covered by rules can never fire in the middle of a longer phrase the
// caller intended to normalize as a whole.
func NormalizeSynonyms(text string, synonyms []Synonym) string {
	if len(synonyms) == 0 {
		return text
	}
	ordered := make([]Synonym, len(synonyms))
	copy(ordered, synonyms)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].Phrase) > len(ordered[j].Phrase)
	})

	out := text
	for _, syn := range ordered {
		if strings.TrimSpace(syn.Phrase) == "" {
			continue
		}
		out = replaceTerm(out, syn.Phrase, syn.Canonical)
	}
	return out
}
