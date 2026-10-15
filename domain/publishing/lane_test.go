package publishing

import "testing"

func liveMetadata(milestone string) map[string]string {
	return map[string]string{"match_id": "game-1", "match_home_team": "SPR", "match_away_team": "SHE", "match_phase": "halftime", "match_state_kind": "live", "match_milestone": milestone}
}

func TestLivePolicyRequiresAllowlistedStructuredSourceAndMilestone(t *testing.T) {
	p := LivePolicy{Enabled: true, SourceAllowlist: map[string]bool{"rss:score": true}, Milestones: map[string]bool{"halftime": true}}
	if got := p.LaneFor("rss:score", liveMetadata("halftime")); got != LaneLive {
		t.Fatalf("got %q, want live", got)
	}
	if got := p.LaneFor("rss:other", liveMetadata("halftime")); got != LaneNormal {
		t.Fatalf("unauthorized source got %q", got)
	}
	if got := p.LaneFor("rss:score", liveMetadata("big play")); got != LaneNormal {
		t.Fatalf("ambiguous milestone got %q", got)
	}
}

func TestPublicationLaneZeroValueIsNormal(t *testing.T) {
	if got := PublicationLane("").Normalized(); got != LaneNormal {
		t.Fatalf("got %q", got)
	}
}

func liveMetadataPhaseOnly(phase string) map[string]string {
	return map[string]string{"match_id": "game-1", "match_home_team": "SPR", "match_away_team": "SHE", "match_phase": phase, "match_state_kind": "live"}
}

func TestLivePolicyAdmitsOrdinaryInGamePhaseWithoutExplicitMilestone(t *testing.T) {
	p := LivePolicy{Enabled: true, SourceAllowlist: map[string]bool{"rss:score": true}, Milestones: map[string]bool{"q3": true}}
	if got := p.LaneFor("rss:score", liveMetadataPhaseOnly("q3")); got != LaneLive {
		t.Fatalf("got %q, want live for a q3 phase with no match_milestone set", got)
	}
	if got := p.LaneFor("rss:score", liveMetadataPhaseOnly("q2")); got != LaneNormal {
		t.Fatalf("got %q, want normal — q2 isn't in this policy's Milestones", got)
	}
}

func TestLivePolicyNeverAdmitsPregameRegardlessOfMilestones(t *testing.T) {
	p := LivePolicy{Enabled: true, SourceAllowlist: map[string]bool{"rss:score": true}, Milestones: map[string]bool{"pregame": true, "q1": true}}
	if got := p.LaneFor("rss:score", liveMetadataPhaseOnly("pregame")); got != LaneNormal {
		t.Fatalf("got %q, want normal — pregame must never enter the live lane", got)
	}
}
