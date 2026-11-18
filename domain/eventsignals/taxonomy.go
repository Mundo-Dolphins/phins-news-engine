// domain/eventsignals/taxonomy.go
package eventsignals

import "sort"

// TypeFlag is a closed behaviour switch an EventTypeDef can carry.
type TypeFlag string

const (
	// FlagReferential marks an event type that describes an article's
	// relationship to some other event (a rumor, a correction, a quote)
	// rather than the event itself.
	FlagReferential TypeFlag = "referential"
	// FlagUpdateFraming marks an event type that updates an earlier report
	// (confirmation, correction).
	FlagUpdateFraming TypeFlag = "update_framing"
	// FlagTeamLevelNeedsOpponent marks an event type whose team-level
	// coverage only identifies one event once an opponent is known.
	FlagTeamLevelNeedsOpponent TypeFlag = "team_level_needs_opponent"
)

// SignalFlag is a closed behaviour switch a SignalDef can carry.
type SignalFlag string

// FlagSpeculative marks a content signal whose framing is unconfirmed.
const FlagSpeculative SignalFlag = "speculative"

// Precedence says whether a content signal's event type wins over the
// detected action's (before_action) or only applies when no action mapped
// to a type (after_action).
type Precedence string

const (
	PrecedenceBeforeAction Precedence = "before_action"
	PrecedenceAfterAction  Precedence = "after_action"
)

// EventTypeDef declares one event type. MemberRole is empty or one of
// "correction", "confirmation", "analysis", "reaction".
type EventTypeDef struct {
	Name        EventType
	Description string
	Flags       []TypeFlag
	MemberRole  string
}

// ActionDef maps an Action to its EventType. BodyFallbackRank > 0 makes the
// action eligible for body-text fallback detection, lower ranks first; 0
// means it is never detected from the body.
type ActionDef struct {
	Name             Action
	EventType        EventType
	BodyFallbackRank int
}

// SignalDef maps a ContentSignal to its EventType and precedence.
type SignalDef struct {
	Name       ContentSignal
	EventType  EventType
	Precedence Precedence
	Flags      []SignalFlag
}

// Taxonomy is the configurable event vocabulary Extract classifies with.
// The order of each list is its detection priority. The zero value knows
// no types: everything classifies as EventTypeGeneric.
type Taxonomy struct {
	EventTypes []EventTypeDef
	Actions    []ActionDef
	Signals    []SignalDef
}

// Classify picks a single EventType from the detected action and content
// signal: a before_action signal's type first, then the action's type,
// then an after_action signal's type, else EventTypeGeneric. Actions and
// signals the taxonomy does not declare are ignored.
func (t Taxonomy) Classify(a Action, s ContentSignal) EventType {
	sig, hasSig := t.signal(s)
	if hasSig && sig.Precedence == PrecedenceBeforeAction {
		return sig.EventType
	}
	if a != "" {
		for _, def := range t.Actions {
			if def.Name == a {
				return def.EventType
			}
		}
	}
	if hasSig && sig.Precedence == PrecedenceAfterAction {
		return sig.EventType
	}
	return EventTypeGeneric
}

// SignalPrecedence returns the precedence declared for s, or "" when the
// taxonomy does not declare it.
func (t Taxonomy) SignalPrecedence(s ContentSignal) Precedence {
	def, _ := t.signal(s)
	return def.Precedence
}

// ActionPriority returns every declared Action in detection priority order.
// The slice is a fresh copy.
func (t Taxonomy) ActionPriority() []Action {
	out := make([]Action, 0, len(t.Actions))
	for _, def := range t.Actions {
		out = append(out, def.Name)
	}
	return out
}

// BodyFallbackPriority returns the actions eligible for body-text fallback
// (BodyFallbackRank > 0), sorted by rank. The slice is a fresh copy.
func (t Taxonomy) BodyFallbackPriority() []Action {
	var ranked []ActionDef
	for _, def := range t.Actions {
		if def.BodyFallbackRank > 0 {
			ranked = append(ranked, def)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].BodyFallbackRank < ranked[j].BodyFallbackRank
	})
	out := make([]Action, 0, len(ranked))
	for _, def := range ranked {
		out = append(out, def.Name)
	}
	return out
}

// SignalPriority returns every declared ContentSignal in detection priority
// order. The slice is a fresh copy.
func (t Taxonomy) SignalPriority() []ContentSignal {
	out := make([]ContentSignal, 0, len(t.Signals))
	for _, def := range t.Signals {
		out = append(out, def.Name)
	}
	return out
}

// EventTypeNames returns every declared EventType in declared order, with
// EventTypeGeneric last. The slice is a fresh copy.
func (t Taxonomy) EventTypeNames() []EventType {
	out := make([]EventType, 0, len(t.EventTypes)+1)
	for _, def := range t.EventTypes {
		out = append(out, def.Name)
	}
	return append(out, EventTypeGeneric)
}

// HasFlag reports whether event type et is declared with flag f.
func (t Taxonomy) HasFlag(et EventType, f TypeFlag) bool {
	def, ok := t.eventType(et)
	if !ok {
		return false
	}
	for _, flag := range def.Flags {
		if flag == f {
			return true
		}
	}
	return false
}

// SignalHasFlag reports whether content signal s is declared with flag f.
func (t Taxonomy) SignalHasFlag(s ContentSignal, f SignalFlag) bool {
	def, ok := t.signal(s)
	if !ok {
		return false
	}
	for _, flag := range def.Flags {
		if flag == f {
			return true
		}
	}
	return false
}

// SignalEventType returns the EventType content signal s maps to, or "" when
// s is not declared.
func (t Taxonomy) SignalEventType(s ContentSignal) EventType {
	def, _ := t.signal(s)
	return def.EventType
}

// MemberRole returns event type et's member role, or "" when it has none or
// is not declared.
func (t Taxonomy) MemberRole(et EventType) string {
	def, _ := t.eventType(et)
	return def.MemberRole
}

// Description returns event type et's description, or "" when it is not
// declared.
func (t Taxonomy) Description(et EventType) string {
	def, _ := t.eventType(et)
	return def.Description
}

func (t Taxonomy) eventType(et EventType) (EventTypeDef, bool) {
	for _, def := range t.EventTypes {
		if def.Name == et {
			return def, true
		}
	}
	return EventTypeDef{}, false
}

func (t Taxonomy) signal(s ContentSignal) (SignalDef, bool) {
	if s == "" {
		return SignalDef{}, false
	}
	for _, def := range t.Signals {
		if def.Name == s {
			return def, true
		}
	}
	return SignalDef{}, false
}
