// Package dictionary defines the keyword vocabulary used to detect whether
// content relates to the covered team: entries with provenance metadata,
// grouped into categories that carry the scoring policy (weight,
// strong-override, ambiguity requirements). classification.Decide applies
// that policy; this package only models the vocabulary and (via Matcher)
// finds it in text.
package dictionary

import "time"

// EntryType classifies what kind of thing an Entry names.
type EntryType string

const (
	EntryTypeOfficialName EntryType = "official_name"
	EntryTypeAbbreviation EntryType = "abbreviation"
	EntryTypeHashtag      EntryType = "hashtag"
	EntryTypeVenue        EntryType = "venue"
	EntryTypeOwner        EntryType = "owner"
	EntryTypeFrontOffice  EntryType = "front_office"
	EntryTypeCoach        EntryType = "coach"
	EntryTypePerson       EntryType = "person"
	EntryTypeAlumnus      EntryType = "alumnus"
	EntryTypeRival        EntryType = "rival"
	EntryTypeNFLContext   EntryType = "nfl_context"
	EntryTypeAmbiguous    EntryType = "ambiguous_term"
	EntryTypeNegative     EntryType = "negative"
	EntryTypeURLPattern   EntryType = "url_pattern"
)

type RosterStatus string

const (
	RosterStatusActive         RosterStatus = "active"
	RosterStatusPracticeSquad  RosterStatus = "practice_squad"
	RosterStatusInjuredReserve RosterStatus = "injured_reserve"
	RosterStatusReserveOther   RosterStatus = "reserve_other"
)

// Entry is a single versioned vocabulary entity.
type Entry struct {
	CanonicalName string
	Aliases       []string
	Type          EntryType
	RosterStatus  RosterStatus
	Role          string
	Position      string
	Number        string
	// ActiveSince/ActiveUntil are informational this phase (not used by
	// classification.Decide) — see design spec "Fuera de alcance".
	ActiveSince *time.Time
	ActiveUntil *time.Time
	Source      string
	UpdatedAt   time.Time
	Confidence  float64
	Notes       string
}

// Category groups Entries under one scoring policy: how much a match is
// worth, whether a single match should immediately decide relatedness
// (StrongOverride), and which other categories must also have matched for
// this one to count at all (RequiresAnyOf — the ambiguity mechanism: e.g.
// "isotopes" alone shouldn't count, "isotopes" + an NFL context term
// should).
type Category struct {
	Name           string
	Weight         float64
	StrongOverride bool
	RequiresAnyOf  []string
	Entries        []Entry
}

// Decision is the threshold policy versioned together with a Dictionary's
// vocabulary.
type Decision struct {
	PositiveThresholdHigh   float64
	PositiveThresholdMedium float64
	NegativeThresholdHigh   float64
	TitleWeightMultiplier   float64
	MaxCountPerTerm         int
}

// Dictionary is the full, versioned vocabulary + policy loaded from a
// dictionary YAML file.
type Dictionary struct {
	Version       string
	SchemaVersion int
	Checksum      string
	Sources       []string
	SnapshotRef   string
	ChangeCount   int
	Tool          string
	Name          string
	Language      string
	GeneratedAt   time.Time
	Categories    []Category
	Decision      Decision
}

// Registration records metadata about a stored dictionary version. It is
// distinct from Dictionary itself: Dictionary is the loaded, in-memory
// vocabulary; Registration is what gets persisted so processing events can
// reference which version made a decision.
type Registration struct {
	Version    string
	Path       string
	EntryCount int
	PromotedAt time.Time
}

// Promoted reports whether this dictionary version is the one currently in
// production use. Promoted versions are never deleted by retention.
func (r Registration) Promoted() bool {
	return !r.PromotedAt.IsZero()
}
