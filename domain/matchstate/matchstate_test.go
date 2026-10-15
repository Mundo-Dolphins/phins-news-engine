package matchstate

import "testing"

func TestFromMetadataRequiresStructuredEvidence(t *testing.T) {
	if _, ok := FromMetadata(map[string]string{"match_phase": "final"}); ok {
		t.Fatal("title-like phase evidence must not create a match identity")
	}
	state, ok := FromMetadata(map[string]string{
		"match_id": "nfl-2025-wk-16-spr-hc", "match_home_team": "SPR",
		"match_away_team": "HC", "match_phase": "final", "match_state_kind": "live",
	})
	if !ok || !state.IsTerminal() {
		t.Fatalf("FromMetadata() = %+v, %v", state, ok)
	}
}

func TestPhaseOrder(t *testing.T) {
	final, _ := FromMetadata(map[string]string{"match_id": "1", "match_home_team": "SPR", "match_away_team": "HC", "match_phase": "final", "match_state_kind": "live"})
	kickoff, _ := FromMetadata(map[string]string{"match_id": "1", "match_home_team": "SPR", "match_away_team": "HC", "match_phase": "kickoff", "match_state_kind": "live"})
	if !kickoff.Precedes(final) {
		t.Fatal("kickoff must precede final")
	}
}
