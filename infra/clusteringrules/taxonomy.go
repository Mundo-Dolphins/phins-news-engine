// infra/clusteringrules/taxonomy.go
package clusteringrules

import (
	"fmt"
	"regexp"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// slugPattern is the shape every taxonomy name must have.
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// The closed sets the taxonomy section may use.
var (
	knownTypeFlags = map[eventsignals.TypeFlag]bool{
		eventsignals.FlagReferential:            true,
		eventsignals.FlagUpdateFraming:          true,
		eventsignals.FlagTeamLevelNeedsOpponent: true,
	}
	knownSignalFlags = map[eventsignals.SignalFlag]bool{
		eventsignals.FlagSpeculative: true,
	}
	knownPrecedences = map[eventsignals.Precedence]bool{
		eventsignals.PrecedenceBeforeAction: true,
		eventsignals.PrecedenceAfterAction:  true,
	}
	knownMemberRoles = map[string]bool{
		"correction": true, "confirmation": true, "analysis": true, "reaction": true,
	}
)

// validateTaxonomy checks the taxonomy section: at least one event type,
// slug-shaped and unique names per list, generic never declared nor used
// as a mapping target, every action/signal mapped to a declared event type,
// flags, precedences and member roles from their closed sets, and unique,
// positive body_fallback ranks.
func validateTaxonomy(t eventsignals.Taxonomy) error {
	if len(t.EventTypes) == 0 {
		return fmt.Errorf("taxonomy is required")
	}

	declared := make(map[eventsignals.EventType]bool, len(t.EventTypes))
	for _, et := range t.EventTypes {
		if err := checkName("taxonomy.event_types", string(et.Name), declared[et.Name]); err != nil {
			return err
		}
		if et.Name == eventsignals.EventTypeGeneric {
			return fmt.Errorf("taxonomy.event_types: %q is reserved and must not be declared", et.Name)
		}
		declared[et.Name] = true
		for _, fl := range et.Flags {
			if !knownTypeFlags[fl] {
				return fmt.Errorf("taxonomy.event_types[%s]: unknown flag %q", et.Name, fl)
			}
		}
		if et.MemberRole != "" && !knownMemberRoles[et.MemberRole] {
			return fmt.Errorf("taxonomy.event_types[%s]: unknown member_role %q", et.Name, et.MemberRole)
		}
	}

	seenActions := make(map[eventsignals.Action]bool, len(t.Actions))
	ranks := make(map[int]eventsignals.Action, len(t.Actions))
	for _, a := range t.Actions {
		if err := checkName("taxonomy.actions", string(a.Name), seenActions[a.Name]); err != nil {
			return err
		}
		seenActions[a.Name] = true
		if err := checkTarget(fmt.Sprintf("taxonomy.actions[%s]", a.Name), a.EventType, declared); err != nil {
			return err
		}
		if a.BodyFallbackRank < 0 {
			return fmt.Errorf("taxonomy.actions[%s]: body_fallback must be positive, got %d", a.Name, a.BodyFallbackRank)
		}
		if a.BodyFallbackRank > 0 {
			if prev, dup := ranks[a.BodyFallbackRank]; dup {
				return fmt.Errorf("taxonomy.actions[%s]: duplicate body_fallback %d (already used by %q)", a.Name, a.BodyFallbackRank, prev)
			}
			ranks[a.BodyFallbackRank] = a.Name
		}
	}

	seenSignals := make(map[eventsignals.ContentSignal]bool, len(t.Signals))
	for _, s := range t.Signals {
		if err := checkName("taxonomy.content_signals", string(s.Name), seenSignals[s.Name]); err != nil {
			return err
		}
		seenSignals[s.Name] = true
		section := fmt.Sprintf("taxonomy.content_signals[%s]", s.Name)
		if err := checkTarget(section, s.EventType, declared); err != nil {
			return err
		}
		if !knownPrecedences[s.Precedence] {
			return fmt.Errorf("%s: unknown precedence %q", section, s.Precedence)
		}
		for _, fl := range s.Flags {
			if !knownSignalFlags[fl] {
				return fmt.Errorf("%s: unknown flag %q", section, fl)
			}
		}
	}
	return nil
}

func checkName(section, name string, seen bool) error {
	if !slugPattern.MatchString(name) {
		return fmt.Errorf("%s: name %q must match %s", section, name, slugPattern)
	}
	if seen {
		return fmt.Errorf("%s: duplicate name %q", section, name)
	}
	return nil
}

func checkTarget(section string, et eventsignals.EventType, declared map[eventsignals.EventType]bool) error {
	if et == eventsignals.EventTypeGeneric {
		return fmt.Errorf("%s: event_type %q is reserved and cannot be a mapping target", section, et)
	}
	if !declared[et] {
		return fmt.Errorf("%s: event_type %q is not declared", section, et)
	}
	return nil
}
