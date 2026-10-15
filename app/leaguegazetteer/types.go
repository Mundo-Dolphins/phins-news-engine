// app/leaguegazetteer/types.go
package leaguegazetteer

// Player is one roster player, independent of the file format.
type Player struct {
	FullName, FirstName, LastName, FootballName string
	TeamAbbr, Position, Status, GSISID          string
}

// TeamRule declares one NFL team and the aliases that resolve to it.
type TeamRule struct {
	Abbr string
	Name string
	// Aliases are nicknames and informal forms in any case ("Topes").
	Aliases []string
	// UpperAbbrs are abbreviations accepted only when the mention is written
	// in capitals ("WAV").
	UpperAbbrs []string
}

// Correction is a manual override: Mention resolves to the player with GSISID,
// or never resolves when GSISID is "none".
type Correction struct{ Mention, GSISID string }

// Rules is the versioned, human-curated side of the gazetteer.
type Rules struct {
	Teams       []TeamRule
	CommonWords []string // surnames never accepted as an alias on their own
	Corrections []Correction
	// UndecidedTerms are words or phrases ("OG", "football team") that mark a
	// team mention as possibly NFL even though they match no alias: such a
	// mention is never rejected as not_an_nfl_team.
	UndecidedTerms []string
	// NoiseMinLetters is the minimum letter count of a real mention (0 = 3).
	NoiseMinLetters int
}

// Status is the outcome class of a player resolution.
type Status string

const (
	Resolved  Status = "resolved"  // decided: exactly one player, strong basis
	Suggested Status = "suggested" // a likely player, not enough to decide
	Ambiguous Status = "ambiguous" // several players could be meant
	NotFound  Status = "not_found"
)

// MatchKind records why a resolution was reached.
type MatchKind string

const (
	KindFullName      MatchKind = "full_name"
	KindUniqueSurname MatchKind = "unique_surname"
	KindTypo          MatchKind = "typo"
	KindInitials      MatchKind = "initials"
	KindSuffix        MatchKind = "suffix"
	KindSurnameShared MatchKind = "surname_shared"
	KindCorrection    MatchKind = "correction"
	KindChooser       MatchKind = "chooser"
)

// Resolution is the result of resolving one mention.
type Resolution struct {
	Status     Status
	Kind       MatchKind
	Candidates []Player // Resolved: exactly one. Suggested/Ambiguous: best first
	Reason     string
}

// Context carries the already-normalised text (see NormalizeText) of the
// article(s) the mention came from; empty means no context.
type Context struct{ Text string }

// TeamStatus is the outcome class of a team resolution.
type TeamStatus string

const (
	TeamResolved  TeamStatus = "resolved"  // the mention is an alias of one NFL team
	TeamUndecided TeamStatus = "undecided" // city/region only, or an abbreviation shared by teams
	TeamNotNFL    TeamStatus = "not_nfl"   // matches nothing in the closed set of 32
)

// TeamResolution is the result of resolving a team mention.
type TeamResolution struct {
	Status TeamStatus
	Abbr   string
	Name   string
}
