// app/entitydiscovery/verify_test.go
package entitydiscovery_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func TestShouldVerify(t *testing.T) {
	full := map[string]string{"position": "QB", "team": "Capitals", "role": "HC", "sport": "football", "league": "NFL"}
	with := func(kind entityproposal.Kind, et entityproposal.EntityType, conf float64, md map[string]string) entityproposal.Proposal {
		return entityproposal.Proposal{Kind: kind, EntityType: et, Confidence: conf, Metadata: md}
	}
	tests := []struct {
		name string
		p    entityproposal.Proposal
		want bool
	}{
		{"high confidence team new_entity", with(entityproposal.KindNewEntity, entityproposal.EntityTypeTeam, 0.99, full), true},
		{"high confidence possible_duplicate", with(entityproposal.KindPossibleDuplicate, entityproposal.EntityTypePlayer, 0.99, full), true},
		{"complete player new_alias high confidence", with(entityproposal.KindNewAlias, entityproposal.EntityTypePlayer, 0.95, full), false},
		{"new_alias low confidence", with(entityproposal.KindNewAlias, entityproposal.EntityTypePlayer, 0.5, full), true},
		{"new_alias player missing position", with(entityproposal.KindNewAlias, entityproposal.EntityTypePlayer, 0.95, map[string]string{"team": "x", "sport": "s", "league": "l"}), true},
		{"new_alias player empty team value", with(entityproposal.KindNewAlias, entityproposal.EntityTypePlayer, 0.95, map[string]string{"position": "QB", "team": "", "sport": "s", "league": "l"}), true},
		{"new_alias verified free agent is complete", with(entityproposal.KindNewAlias, entityproposal.EntityTypePlayer, 0.95, map[string]string{"position": "WR", "affiliation_status": "free_agent", "sport": "football", "league": "NFL"}), false},
		{"new_alias free agent missing position", with(entityproposal.KindNewAlias, entityproposal.EntityTypePlayer, 0.95, map[string]string{"affiliation_status": "free_agent", "sport": "football", "league": "NFL"}), true},
		{"new_alias unknown affiliation still verifies", with(entityproposal.KindNewAlias, entityproposal.EntityTypePlayer, 0.95, map[string]string{"position": "WR", "affiliation_status": "unknown", "sport": "football", "league": "NFL"}), true},
		{"new_alias free agent coach still needs team", with(entityproposal.KindNewAlias, entityproposal.EntityTypeCoach, 0.95, map[string]string{"role": "HC", "affiliation_status": "free_agent", "sport": "football", "league": "NFL"}), true},
		{"new_alias coach missing role", with(entityproposal.KindNewAlias, entityproposal.EntityTypeCoach, 0.95, map[string]string{"team": "x", "sport": "s", "league": "l"}), true},
		{"new_alias coach complete", with(entityproposal.KindNewAlias, entityproposal.EntityTypeCoach, 0.95, full), false},
		{"new_alias team", with(entityproposal.KindNewAlias, entityproposal.EntityTypeTeam, 0.95, full), true},
		{"new_alias missing league", with(entityproposal.KindNewAlias, entityproposal.EntityTypeExecutive, 0.95, map[string]string{"role": "GM", "team": "x", "sport": "s"}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := entitydiscovery.ShouldVerify(tt.p, 0.85); got != tt.want {
				t.Fatalf("ShouldVerify = %v, want %v", got, tt.want)
			}
		})
	}
}
