package entitydiscovery_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func TestAffiliationLabel(t *testing.T) {
	player := entityproposal.EntityTypePlayer
	cases := []struct {
		name string
		p    entityproposal.Proposal
		want string
	}{
		{"free agent", entityproposal.Proposal{EntityType: player, Metadata: map[string]string{entitydiscovery.MetadataAffiliationStatus: "free_agent"}}, "FREE AGENT"},
		{"explicit unknown", entityproposal.Proposal{EntityType: player, Metadata: map[string]string{entitydiscovery.MetadataAffiliationStatus: "unknown", "team": "Springfield"}}, "UNKNOWN"},
		{"no team at all", entityproposal.Proposal{EntityType: player}, "UNKNOWN"},
		{"team name", entityproposal.Proposal{EntityType: player, Metadata: map[string]string{"team": "Springfield"}}, ""},
		{"team id", entityproposal.Proposal{EntityType: player, Metadata: map[string]string{"team_id": "springfield-isotopes"}}, ""},
		{"non player", entityproposal.Proposal{EntityType: entityproposal.EntityTypeCoach}, ""},
	}
	for _, c := range cases {
		if got := entitydiscovery.AffiliationLabel(c.p); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
