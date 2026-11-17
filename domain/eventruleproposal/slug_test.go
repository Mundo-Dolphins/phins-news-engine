// domain/eventruleproposal/slug_test.go
package eventruleproposal

import "testing"

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"player injury status":    "player-injury-status",
		"player_injury_status":    "player-injury-status",
		" Player Injury Status":   "player-injury-status",
		"qb change/debut":         "qb-change-debut",
		"coaching/film breakdown": "coaching-film-breakdown",
		"Rookie Débutant":         "rookie-débutant",
		"///":                     "",
		"":                        "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
