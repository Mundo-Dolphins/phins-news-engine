// Package knowledge defines the provider-neutral shapes an external
// structured-knowledge source (currently Wikidata) returns to
// the offline entity-discovery tooling. Nothing here knows about any one
// provider's wire format, and nothing here decides NFL relevance.
package knowledge

import "time"

// Rank is a statement's provider-assigned rank.
type Rank string

const (
	RankPreferred  Rank = "preferred"
	RankNormal     Rank = "normal"
	RankDeprecated Rank = "deprecated"
)

// ValueKind says which Value field is meaningful.
type ValueKind string

const (
	KindEntity    ValueKind = "entity"
	KindString    ValueKind = "string"
	KindTime      ValueKind = "time"
	KindQuantity  ValueKind = "quantity"
	KindNoValue   ValueKind = "novalue"
	KindSomeValue ValueKind = "somevalue"
	KindOther     ValueKind = "other"
)

// Value is one statement or qualifier value. Time is the earliest instant
// of the stated period; Precision is the provider's precision code (for
// Wikidata: 9 = year, 10 = month, 11 = day). A year- or month-precision
// end time resolves to the start of that period, so ValidAt reports the
// statement as ended for the whole period; Precision is kept so callers
// can reason about it.
type Value struct {
	Kind      ValueKind  `json:"kind"`
	EntityID  string     `json:"entity_id,omitempty"`
	Text      string     `json:"text,omitempty"`
	Time      *time.Time `json:"time,omitempty"`
	Precision int        `json:"precision,omitempty"`
}

// Qualifier narrows a statement (for example its start or end time). The
// Property is an opaque provider property ID; this package never
// interprets it.
type Qualifier struct {
	Property string `json:"property"`
	Value    Value  `json:"value"`
}

// Statement is one claim about an entity. ValidFrom/ValidTo are derived
// by the adapter from the provider's start/end-time qualifiers; a nil
// bound means open (unknown, no value, or absent).
type Statement struct {
	Property   string      `json:"property"`
	Value      Value       `json:"value"`
	Rank       Rank        `json:"rank"`
	Qualifiers []Qualifier `json:"qualifiers,omitempty"`
	ValidFrom  *time.Time  `json:"valid_from,omitempty"`
	ValidTo    *time.Time  `json:"valid_to,omitempty"`
}

// ValidAt reports whether the statement holds at t: not deprecated, t not
// before ValidFrom, and t strictly before ValidTo.
func (s Statement) ValidAt(t time.Time) bool {
	if s.Rank == RankDeprecated {
		return false
	}
	if s.ValidFrom != nil && t.Before(*s.ValidFrom) {
		return false
	}
	if s.ValidTo != nil && !t.Before(*s.ValidTo) {
		return false
	}
	return true
}

// Candidate is one search hit. Callers must not assume the first
// candidate is the right one.
type Candidate struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// Entity is the normalized record for one provider item. ID is always the
// ID the caller asked for; CanonicalID is set only when the provider
// resolved that ID to a different one (for example a merged item).
type Entity struct {
	ID          string      `json:"id"`
	CanonicalID string      `json:"canonical_id,omitempty"`
	Label       string      `json:"label"`
	Description string      `json:"description,omitempty"`
	Aliases     []string    `json:"aliases,omitempty"`
	Statements  []Statement `json:"statements,omitempty"`
}
