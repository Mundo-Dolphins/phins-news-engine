// app/leaguegazetteer/team_test.go
package leaguegazetteer_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
)

func TestResolveTeam(t *testing.T) {
	idx := newIndex(t)
	cases := []struct {
		mention string
		status  leaguegazetteer.TeamStatus
		abbr    string
	}{
		{"Topes", leaguegazetteer.TeamResolved, "SPR"},
		{"isos", leaguegazetteer.TeamResolved, "SPR"},
		{"Springfield Isotopes", leaguegazetteer.TeamResolved, "SPR"},
		{"Eighters", leaguegazetteer.TeamResolved, "HC"},
		{"88ers", leaguegazetteer.TeamResolved, "HC"},
		{"SPR", leaguegazetteer.TeamResolved, "SPR"},
		{"spr", leaguegazetteer.TeamUndecided, ""},         // a lowercase abbreviation is not an alias, but may still be a team
		{"LA", leaguegazetteer.TeamUndecided, ""},          // shared by two teams, never rejected
		{"Springfield", leaguegazetteer.TeamUndecided, ""}, // city only: could be the Tempests
		{"Coastline", leaguegazetteer.TeamUndecided, ""},   // two teams
		{"Waverly", leaguegazetteer.TeamUndecided, ""},
		{"Northern", leaguegazetteer.TeamNotNFL, ""},
		{"Harvesters", leaguegazetteer.TeamNotNFL, ""},
		{"", leaguegazetteer.TeamNotNFL, ""},
		{"Herons fans", leaguegazetteer.TeamUndecided, ""},   // a team nickname inside a longer name: never rejected
		{"Waverly State", leaguegazetteer.TeamUndecided, ""}, // a city word inside a longer name
		{"la", leaguegazetteer.TeamUndecided, ""},            // lowercase abbreviation shared by teams
		{"Northern Weasels", leaguegazetteer.TeamNotNFL, ""},
	}
	for _, tc := range cases {
		got := idx.ResolveTeam(tc.mention)
		if got.Status != tc.status || got.Abbr != tc.abbr {
			t.Errorf("ResolveTeam(%q) = %+v, want %s/%q", tc.mention, got, tc.status, tc.abbr)
		}
	}
	if got := idx.ResolveTeam("Topes"); got.Name != "Springfield Isotopes" {
		t.Errorf("Name = %q, want the configured team name", got.Name)
	}
}

func TestResolveTeam_UndecidedTermsAreNeverRejected(t *testing.T) {
	rules := testRules()
	rules.UndecidedTerms = []string{"OG", "football team"}
	idx, err := leaguegazetteer.New(testPlayers(), rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, mention := range []string{"OG", "og goliaths fans", "Westbridge Football Team"} {
		if got := idx.ResolveTeam(mention); got.Status != leaguegazetteer.TeamUndecided {
			t.Errorf("ResolveTeam(%q) = %+v, want undecided", mention, got)
		}
	}
}

func TestResolveTeam_SingularNicknamesResolve(t *testing.T) {
	idx := newIndex(t)
	for mention, abbr := range map[string]string{"Comet": "CYP", "Heron": "WAV", "Ocelot": "COB", "88er": "HC", "Condor": "MES", "Isotope": "SPR"} {
		if got := idx.ResolveTeam(mention); got.Status != leaguegazetteer.TeamResolved || got.Abbr != abbr {
			t.Errorf("ResolveTeam(%q) = %+v, want resolved %s", mention, got, abbr)
		}
	}
}
