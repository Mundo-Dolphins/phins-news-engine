// app/leaguegazetteer/player_test.go
package leaguegazetteer_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
)

func newIndex(t *testing.T) *leaguegazetteer.Index {
	t.Helper()
	idx, err := leaguegazetteer.New(testPlayers(), testRules())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return idx
}

func ctxOf(parts ...string) leaguegazetteer.Context {
	return leaguegazetteer.Context{Text: leaguegazetteer.NormalizeText(parts...)}
}

func TestResolvePlayer(t *testing.T) {
	idx := newIndex(t)
	cases := []struct {
		name    string
		mention string
		ctx     leaguegazetteer.Context
		status  leaguegazetteer.Status
		kind    leaguegazetteer.MatchKind
		gsis    string // expected sole/best candidate, "" = don't check
	}{
		{"T1 exact full name", "dorian burns", ctxOf(), leaguegazetteer.Resolved, leaguegazetteer.KindFullName, "g-burns"},
		{"T1 punctuation insensitive", "Ja'Ron Quillfeather", ctxOf(), leaguegazetteer.Resolved, leaguegazetteer.KindFullName, "g-quillfeather"},
		{"T1 suffixed mention, suffixed player", "Orvel Harrowgate Jr.", ctxOf(), leaguegazetteer.Resolved, leaguegazetteer.KindFullName, "g-harrowgate"},
		{"unsuffixed mention is not promoted to the Jr.", "Orvel Harrowgate", ctxOf(), leaguegazetteer.Suggested, leaguegazetteer.KindSuffix, "g-harrowgate"},
		{"Sr. mention never resolves to the unsuffixed player", "Arlo Venwick Sr.", ctxOf(), leaguegazetteer.Suggested, leaguegazetteer.KindSuffix, "g-avenwick"},
		{"suffix the roster omits is only a suggestion", "Garrick Mossvale II", ctxOf(), leaguegazetteer.Suggested, leaguegazetteer.KindSuffix, "g-mossvale"},
		{"homonyms are ambiguous", "Jaylen Smith", ctxOf(), leaguegazetteer.Ambiguous, leaguegazetteer.KindFullName, ""},
		{"T2b typo with a unique candidate", "Aldous Dunmorrow", ctxOf(), leaguegazetteer.Resolved, leaguegazetteer.KindTypo, "g-dunmarrow"},
		{"truncated token is not a typo", "Orrin Be", ctxOf(), leaguegazetteer.NotFound, "", ""},
		{"a different suffix is never a typo of the same person", "Orvel Harrowgate Sr.", ctxOf(), leaguegazetteer.NotFound, "", ""},
		{"a typo with the matching suffix still resolves", "Orvel Harrowgte Jr.", ctxOf(), leaguegazetteer.Resolved, leaguegazetteer.KindTypo, "g-harrowgate"},
		{"a typo in an unsuffixed mention is not promoted to a Jr.", "Orvel Harrowgte", ctxOf(), leaguegazetteer.NotFound, "", ""},
		{"two edits on a short name is no typo (Bront is not Brandt)", "Tom Bront", ctxOf(), leaguegazetteer.NotFound, "", ""},
		{"a truncated last name is only a suggestion", "Orrin Bel", ctxOf(), leaguegazetteer.Suggested, leaguegazetteer.KindTypo, "g-cbelk"},
		{"T3 initials/prefix variant is only a suggestion", "AD Mossgrove", ctxOf(), leaguegazetteer.Suggested, leaguegazetteer.KindInitials, "g-mossgrove"},
		{"T2a unique surname with corroboration", "Burns", ctxOf("Burns leads the Herons"), leaguegazetteer.Resolved, leaguegazetteer.KindUniqueSurname, "g-burns"},
		{"T2a corroborated by the full name in the text", "Kestrell", ctxOf("Tobin Kestrell caught it"), leaguegazetteer.Resolved, leaguegazetteer.KindUniqueSurname, "g-kestrell"},
		{"unique surname without context is only a suggestion", "Burns", ctxOf(), leaguegazetteer.Suggested, leaguegazetteer.KindUniqueSurname, "g-burns"},
		{"unique surname with the wrong team in the text is only a suggestion", "Fennimore", ctxOf("Herons win"), leaguegazetteer.Suggested, leaguegazetteer.KindUniqueSurname, "g-fennimore"},
		{"surname shared by 3 players is ambiguous, team mention first", "Brackwater", ctxOf("Isotopes safeties"), leaguegazetteer.Ambiguous, leaguegazetteer.KindSurnameShared, ""},
		{"stop-word surname is not an alias", "All", ctxOf("Ocelots"), leaguegazetteer.NotFound, "", ""},
		{"short token is not an alias", "Jon", ctxOf(), leaguegazetteer.NotFound, "", ""},
		{"first-name-only mention never matches", "Aldous", ctxOf("Isotopes"), leaguegazetteer.NotFound, "", ""},
		{"retired player absent from the roster", "Buchanan", ctxOf("Capitals"), leaguegazetteer.NotFound, "", ""},
		{"empty mention", "  ", ctxOf(), leaguegazetteer.NotFound, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := idx.ResolvePlayer(tc.mention, tc.ctx)
			if got.Status != tc.status || (tc.kind != "" && got.Kind != tc.kind) {
				t.Fatalf("ResolvePlayer(%q) = %s/%s %+v, want %s/%s", tc.mention, got.Status, got.Kind, got.Candidates, tc.status, tc.kind)
			}
			if tc.gsis != "" && (len(got.Candidates) == 0 || got.Candidates[0].GSISID != tc.gsis) {
				t.Fatalf("ResolvePlayer(%q) first candidate = %+v, want %s", tc.mention, got.Candidates, tc.gsis)
			}
			if got.Status == leaguegazetteer.Resolved && len(got.Candidates) != 1 {
				t.Fatalf("a Resolved outcome must name exactly one player, got %d", len(got.Candidates))
			}
		})
	}
}

func TestResolvePlayer_AmbiguousSurnamePutsTheMentionedTeamFirstAndCapsAtEight(t *testing.T) {
	idx := newIndex(t)
	got := idx.ResolvePlayer("Brackwater", ctxOf("Isotopes safeties"))
	if len(got.Candidates) != 3 || got.Candidates[0].TeamAbbr != "SPR" {
		t.Fatalf("candidates = %+v, want 3 with a SPR player first", got.Candidates)
	}
	many := make([]leaguegazetteer.Player, 0, 12)
	for n := 0; n < 12; n++ {
		many = append(many, p("Pat Lewis", "Pat", "Lewis", "CAP", "WR", "ACT", string(rune('a'+n))))
	}
	wide, err := leaguegazetteer.New(many, testRules())
	if err != nil {
		t.Fatal(err)
	}
	if got := wide.ResolvePlayer("Lewis", ctxOf()); len(got.Candidates) != 8 {
		t.Fatalf("candidates = %d, want the cap of 8", len(got.Candidates))
	}
}

func TestResolvePlayer_Corrections(t *testing.T) {
	rules := testRules()
	rules.Corrections = []leaguegazetteer.Correction{{Mention: "Big Burns", GSISID: "g-burns"}, {Mention: "Cosmo Danforth", GSISID: "none"}}
	idx, err := leaguegazetteer.New(testPlayers(), rules)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.ResolvePlayer("big burns", ctxOf()); got.Status != leaguegazetteer.Resolved || got.Kind != leaguegazetteer.KindCorrection || got.Candidates[0].GSISID != "g-burns" {
		t.Fatalf("correction to a player = %+v", got)
	}
	if got := idx.ResolvePlayer("Cosmo Danforth", ctxOf()); got.Status != leaguegazetteer.NotFound {
		t.Fatalf("correction 'none' = %+v, want NotFound", got)
	}
}

func TestCorroborated(t *testing.T) {
	idx := newIndex(t)
	burns, _ := idx.PlayerByGSIS("g-burns")
	for text, want := range map[string]bool{
		"the herons won": true, "dorian burns threw": true, "the birds flew": true,
		"the capitals won": false, "": false, "waverly is a city": false,
	} {
		if got := idx.Corroborated(burns, leaguegazetteer.Context{Text: leaguegazetteer.NormalizeText(text)}); got != want {
			t.Errorf("Corroborated(%q) = %v, want %v", text, got, want)
		}
	}
}
