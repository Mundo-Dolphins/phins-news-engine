package eventsignals_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func TestParseAffiliationStatus(t *testing.T) {
	for in, want := range map[string]eventsignals.AffiliationStatus{
		"": eventsignals.AffiliationUnspecified, "team": eventsignals.AffiliationTeam,
		"free_agent": eventsignals.AffiliationFreeAgent, "unknown": eventsignals.AffiliationUnknown,
	} {
		got, err := eventsignals.ParseAffiliationStatus(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := eventsignals.ParseAffiliationStatus("retired"); err == nil {
		t.Error("Parse(retired) error = nil")
	}
}

func TestValidateAffiliation(t *testing.T) {
	player, team := eventsignals.EntityKindPlayer, eventsignals.EntityKindTeam
	tests := []struct {
		name       string
		kind       eventsignals.EntityKind
		status     eventsignals.AffiliationStatus
		teamID, tm string
		wantErr    bool
	}{
		{"team with team_id", player, eventsignals.AffiliationTeam, "capital-city-capitals", "", false},
		{"team missing team_id", player, eventsignals.AffiliationTeam, "", "Capitals", true},
		{"free agent clean", player, eventsignals.AffiliationFreeAgent, "", "", false},
		{"free agent with team_id", player, eventsignals.AffiliationFreeAgent, "x", "", true},
		{"free agent with team name", player, eventsignals.AffiliationFreeAgent, "", "Capitals", true},
		{"free agent on team entity", team, eventsignals.AffiliationFreeAgent, "", "", true},
		{"free agent on coach", eventsignals.EntityKindCoach, eventsignals.AffiliationFreeAgent, "", "", true},
		{"unknown clean", player, eventsignals.AffiliationUnknown, "", "", false},
		{"unknown with team_id", player, eventsignals.AffiliationUnknown, "x", "", true},
		{"unspecified anything", player, eventsignals.AffiliationUnspecified, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := eventsignals.ValidateAffiliation(tt.kind, tt.status, tt.teamID, tt.tm)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEntityRuleEffectiveAffiliation(t *testing.T) {
	if got := (eventsignals.EntityRule{TeamID: "x"}).EffectiveAffiliation(); got != eventsignals.AffiliationTeam {
		t.Errorf("legacy team_id => %q, want team", got)
	}
	if got := (eventsignals.EntityRule{}).EffectiveAffiliation(); got != eventsignals.AffiliationUnspecified {
		t.Errorf("legacy empty => %q, want unspecified", got)
	}
	if got := (eventsignals.EntityRule{AffiliationStatus: eventsignals.AffiliationFreeAgent}).EffectiveAffiliation(); got != eventsignals.AffiliationFreeAgent {
		t.Errorf("got %q", got)
	}
}
