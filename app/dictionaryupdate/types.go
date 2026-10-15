package dictionaryupdate

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

type SourceKind string

const (
	SourceKindRoster      SourceKind = "roster"
	SourceKindCoaches     SourceKind = "coaches"
	SourceKindFrontOffice SourceKind = "front_office"
)

type AliasClass string

const (
	AliasClassExact             AliasClass = "exact"
	AliasClassTypographic       AliasClass = "typographic"
	AliasClassAbbreviated       AliasClass = "abbreviated"
	AliasClassFirstLast         AliasClass = "first_last"
	AliasClassInitialLast       AliasClass = "initial_last"
	AliasClassConfirmedNickname AliasClass = "confirmed_nickname"
	AliasClassSuggested         AliasClass = "suggested"
	AliasClassAmbiguous         AliasClass = "ambiguous"
)

type AliasCandidate struct {
	Value string
	Class AliasClass
}

// SourceEntity is one person/role observed on an official source page,
// before comparison against the current dictionary.
type SourceEntity struct {
	DisplayName  string
	CanonicalKey string
	Kind         SourceKind
	Role         string
	Position     string
	Number       string
	Department   string
	ProfileURL   string
	Aliases      []AliasCandidate
	RosterStatus dictionary.RosterStatus
	Source       string
	ObservedAt   time.Time
}
