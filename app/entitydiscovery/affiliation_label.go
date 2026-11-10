package entitydiscovery

import (
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// AffiliationLabel labels a player's affiliation when no team line can say
// it: a verified free agent and an unresolved affiliation must read
// differently, never as an empty "Team:". It returns "FREE AGENT",
// "UNKNOWN" or "" (players with a team and any non-player get no label).
func AffiliationLabel(p entityproposal.Proposal) string {
	if p.EntityType != entityproposal.EntityTypePlayer {
		return ""
	}
	switch eventsignals.AffiliationStatus(p.Metadata[MetadataAffiliationStatus]) {
	case eventsignals.AffiliationFreeAgent:
		return "FREE AGENT"
	case eventsignals.AffiliationUnknown:
		return "UNKNOWN"
	}
	if p.Metadata["team"] == "" && p.Metadata["team_id"] == "" {
		return "UNKNOWN"
	}
	return ""
}
