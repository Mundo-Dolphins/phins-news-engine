package entityidentity

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

// Wikidata P54 affiliation semantics: only one safely current,
// locally mapped NFL team yields affiliation_status=team; everything else
// is unknown and never free_agent, with a bounded diagnostic reason.

// sharks is an NFL team that the fake local catalog cannot map.
var sharks = ent("Q910000005", "Shelbyville Sharks", nil,
	item("P31", qFBTeam), item("P641", qFootball), item("P118", qNFL))

func resolveAffiliation(stmts ...knowledge.Statement) Result {
	m := ostrander(stmts...)
	return resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m, sharks})
}

func wantAffiliation(t *testing.T, r Result, status, team, teamID, reason string) {
	t.Helper()
	wantStatus(t, r, StatusVerifiedNFL)
	got := r.Metadata
	if got["affiliation_status"] != status || got["team"] != team || got["team_id"] != teamID {
		t.Errorf("affiliation = %q team=%q team_id=%q, want %q %q %q", got["affiliation_status"], got["team"], got["team_id"], status, team, teamID)
	}
	if status != "team" && got["affiliation_status"] == "free_agent" {
		t.Errorf("missing/ambiguous Wikidata data must never mean free_agent")
	}
	if r.AffiliationReason != reason {
		t.Errorf("AffiliationReason = %q, want %q", r.AffiliationReason, reason)
	}
}

func TestAffiliation_CurrentP54IsTeam(t *testing.T) {
	r := resolveAffiliation(span(member("Q910000007"), year(2017), nil))
	wantAffiliation(t, r, "team", "Capital City Capitals", "capital-city-capitals", "")
}

// verifiedPlayerAffiliation runs the affiliation step for a player whose
// NFL identity is already established by other evidence (Resolve itself
// cannot verify a player that has no live P54 at all).
func verifiedPlayerAffiliation(stmts ...knowledge.Statement) Result {
	m := ostrander(stmts...)
	res := Result{Status: StatusVerifiedNFL, Metadata: map[string]string{}}
	enrichPlayerAffiliation(&res, Input{Catalog: catalog, Entities: world(nflTeams()...), Now: now}, m)
	return res
}

func TestAffiliation_NoP54IsUnknownNotFreeAgent(t *testing.T) {
	wantAffiliation(t, verifiedPlayerAffiliation(), "unknown", "", "", "wikidata_affiliation=no_current_membership")
	// Through Resolve the player is not even verified, and never free_agent.
	r := resolveAffiliation()
	if r.Metadata["affiliation_status"] == "free_agent" || r.Metadata["team_id"] != "" {
		t.Errorf("metadata = %v", r.Metadata)
	}
}

func TestAffiliation_HistoricalOnlyIsUnknown(t *testing.T) {
	r := resolveAffiliation(
		span(member("Q910000015"), year(2014), year(2017)),
		span(member("Q910000007"), year(2023), year(2025)),
	)
	wantAffiliation(t, r, "unknown", "", "", "wikidata_affiliation=historical_only")
}

func TestAffiliation_CurrentBeatsHistorical(t *testing.T) {
	r := resolveAffiliation(
		span(member("Q910000007"), year(2017), year(2021)),
		span(member("Q910000009"), year(2021), nil),
	)
	wantAffiliation(t, r, "team", "Brockway Badgers", "brockway-badgers", "")
}

func TestAffiliation_ConflictingCurrentIsUnknown(t *testing.T) {
	r := resolveAffiliation(
		span(member("Q910000007"), year(2017), nil),
		span(member("Q910000009"), year(2021), nil),
	)
	wantAffiliation(t, r, "unknown", "", "", "wikidata_affiliation=conflicting_current_memberships")
}

func TestAffiliation_DeprecatedCurrentIsIgnored(t *testing.T) {
	r := verifiedPlayerAffiliation(ranked(span(member("Q910000007"), year(2017), nil), knowledge.RankDeprecated))
	wantAffiliation(t, r, "unknown", "", "", "wikidata_affiliation=no_current_membership")
}

func TestAffiliation_UnmappedTeamIsUnknownWithoutFabricatedID(t *testing.T) {
	r := resolveAffiliation(span(member("Q910000005"), year(2020), nil))
	wantAffiliation(t, r, "unknown", "", "", "wikidata_affiliation=unmapped_team")
}

func TestAffiliation_FutureOnlyAndContradictoryWindowsAreUnknown(t *testing.T) {
	future := resolveAffiliation(span(member("Q910000007"), year(2030), nil))
	wantAffiliation(t, future, "unknown", "", "", "wikidata_affiliation=no_current_membership")

	bad := resolveAffiliation(span(member("Q910000007"), year(2025), year(2020)))
	wantAffiliation(t, bad, "unknown", "", "", "wikidata_affiliation=no_current_membership")
}

func TestAffiliation_OnlyPlayersCarryAffiliationStatus(t *testing.T) {
	coach := ent("Q50", "Mike Thistlewood", nil, item("P31", qHuman), item("P106", qFBCoach), item("P641", qFootball), item("P54", "Q910000006"))
	r := resolve("Mike Thistlewood", entityproposal.EntityTypeCoach, candsOf(coach), []knowledge.Entity{coach})
	wantNoMeta(t, r, "affiliation_status")
}
