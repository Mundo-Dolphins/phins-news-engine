package eventsignals

import "fmt"

// AffiliationStatus states explicitly how a person relates to a team, so
// an empty team id never has to stand for "free agent", "unknown" and
// "Isotopes" at the same time.
type AffiliationStatus string

const (
	// AffiliationUnspecified is the legacy/absent value: nothing was
	// declared. It never implies free agency, unknown or Isotopes.
	AffiliationUnspecified AffiliationStatus = ""
	// AffiliationTeam means the current team is known and referenced by id.
	AffiliationTeam AffiliationStatus = "team"
	// AffiliationFreeAgent means a verified player with no current team.
	AffiliationFreeAgent AffiliationStatus = "free_agent"
	// AffiliationUnknown means the current affiliation could not be
	// established; it must not be treated as free agency or a team.
	AffiliationUnknown AffiliationStatus = "unknown"
)

// ParseAffiliationStatus converts s to an AffiliationStatus; "" is
// AffiliationUnspecified and any unrecognized value is an error.
func ParseAffiliationStatus(s string) (AffiliationStatus, error) {
	switch st := AffiliationStatus(s); st {
	case AffiliationUnspecified, AffiliationTeam, AffiliationFreeAgent, AffiliationUnknown:
		return st, nil
	}
	return AffiliationUnspecified, fmt.Errorf("eventsignals: unrecognized affiliation_status %q", s)
}

// ValidateAffiliation checks the shape of an affiliation for an entity of
// the given kind. It does not check that teamID references a real team —
// callers that hold a catalog do that. AffiliationUnspecified is always
// shape-valid (legacy data).
func ValidateAffiliation(kind EntityKind, status AffiliationStatus, teamID, team string) error {
	switch status {
	case AffiliationTeam:
		if teamID == "" {
			return fmt.Errorf("eventsignals: affiliation_status=team requires a team_id")
		}
	case AffiliationFreeAgent:
		if kind != EntityKindPlayer {
			return fmt.Errorf("eventsignals: affiliation_status=free_agent is only valid for players, not %q", kind)
		}
		if teamID != "" || team != "" {
			return fmt.Errorf("eventsignals: affiliation_status=free_agent must have no team or team_id")
		}
	case AffiliationUnknown:
		if teamID != "" {
			return fmt.Errorf("eventsignals: affiliation_status=unknown must have no team_id")
		}
	case AffiliationUnspecified:
	default:
		return fmt.Errorf("eventsignals: unrecognized affiliation_status %q", status)
	}
	return nil
}

// EffectiveAffiliation returns the declared status, reading a legacy
// entity with a team_id and no status as AffiliationTeam. An entity with
// neither stays AffiliationUnspecified: empty never implies free agent.
func (e EntityRule) EffectiveAffiliation() AffiliationStatus {
	if e.AffiliationStatus == AffiliationUnspecified && e.TeamID != "" {
		return AffiliationTeam
	}
	return e.AffiliationStatus
}
