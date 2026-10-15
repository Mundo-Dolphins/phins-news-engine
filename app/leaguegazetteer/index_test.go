// app/leaguegazetteer/index_test.go
package leaguegazetteer_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
)

func testRules() leaguegazetteer.Rules {
	return leaguegazetteer.Rules{
		Teams: []leaguegazetteer.TeamRule{
			{Abbr: "WAV", Name: "Waverly Herons", Aliases: []string{"Herons", "Birds"}, UpperAbbrs: []string{"WAV"}},
			{Abbr: "CAP", Name: "Capital City Capitals", Aliases: []string{"Capitals"}, UpperAbbrs: []string{"CAP"}},
			{Abbr: "COB", Name: "Cobbleton Ocelots", Aliases: []string{"Ocelots"}, UpperAbbrs: []string{"COB"}},
			{Abbr: "CYP", Name: "Cypress Comets", Aliases: []string{"Comets", "Coms"}, UpperAbbrs: []string{"CYP"}},
			{Abbr: "MES", Name: "Mesa Condors", Aliases: []string{"Condors"}, UpperAbbrs: []string{"MES"}},
			{Abbr: "SPR", Name: "Springfield Isotopes", Aliases: []string{"Isotopes", "Topes", "Isos"}, UpperAbbrs: []string{"SPR"}},
			{Abbr: "HC", Name: "Harbor City 88ers", Aliases: []string{"88ers", "Eighters"}, UpperAbbrs: []string{"HC"}},
			{Abbr: "LKV", Name: "Lakeview Foals", Aliases: []string{"Foals"}, UpperAbbrs: []string{"LKV"}},
			{Abbr: "LA", Name: "Coastline Rhinos", Aliases: []string{"Rhinos"}},
			{Abbr: "CLC", Name: "Coastline Stingrays", Aliases: []string{"Stingrays"}, UpperAbbrs: []string{"CLC"}},
		},
		CommonWords: []string{"All", "Dial"},
	}
}

func p(full, first, last, team, pos, status, gsis string) leaguegazetteer.Player {
	return leaguegazetteer.Player{FullName: full, FirstName: first, LastName: last, TeamAbbr: team, Position: pos, Status: status, GSISID: gsis}
}

func testPlayers() []leaguegazetteer.Player {
	return []leaguegazetteer.Player{
		p("Dorian Burns", "Dorian", "Burns", "WAV", "QB", "ACT", "g-burns"),
		p("Tobin Kestrell", "Tobin", "Kestrell", "CAP", "TE", "ACT", "g-kestrell"),
		p("Ja'Ron Quillfeather", "Ja'Ron", "Quillfeather", "COB", "WR", "ACT", "g-quillfeather"),
		p("Fennick All", "Fennick", "All", "COB", "TE", "ACT", "g-all"),
		p("Arlo Venwick", "Arlo", "Venwick", "CYP", "RB", "ACT", "g-avenwick"),
		p("Orvel Harrowgate Jr.", "Orvel", "Harrowgate Jr.", "MES", "WR", "ACT", "g-harrowgate"),
		p("Zorin Brackwater", "Zorin", "Brackwater", "SPR", "S", "ACT", "g-zanderson"),
		p("Teague Brackwater", "Teague", "Brackwater", "SPR", "LB", "ACT", "g-landerson"),
		p("Osric Brackwater", "Osric", "Brackwater", "HC", "DT", "ACT", "g-eanderson"),
		p("Adair Mossgrove", "Adair", "Mossgrove", "LKV", "WR", "ACT", "g-mossgrove"),
		p("Aldous Dunmarrow", "Aldous", "Dunmarrow", "LA", "DT", "RET", "g-dunmarrow"),
		p("Orrin Belk", "Orrin", "Belk", "SPR", "WR", "ACT", "g-cbelk"),
		p("Jaylen Smith", "Jaylen", "Smith", "HC", "WR", "ACT", "g-jsmith1"),
		p("Jaylen Smith", "Jaylen", "Smith", "LKV", "CB", "DEV", "g-jsmith2"),
		p("Garrick Mossvale", "Garrick", "Mossvale", "MES", "QB", "ACT", "g-mossvale"),
		p("Case Fennimore", "Case", "Fennimore", "CAP", "QB", "ACT", "g-fennimore"),
		p("Tom Brandt", "Tom", "Brandt", "CAP", "TE", "ACT", "g-brandt"),
	}
}

func TestNew_AcceptsValidRulesAndPlayers(t *testing.T) {
	idx, err := leaguegazetteer.New(testPlayers(), testRules())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if idx.TeamName("WAV") != "Waverly Herons" || len(idx.TeamAbbrs()) != 10 || idx.MinLetters() != 3 {
		t.Fatalf("accessors: %q %d %d", idx.TeamName("WAV"), len(idx.TeamAbbrs()), idx.MinLetters())
	}
	if got, ok := idx.PlayerByGSIS("g-kestrell"); !ok || got.FullName != "Tobin Kestrell" {
		t.Fatalf("PlayerByGSIS = %+v, %v", got, ok)
	}
}

func TestNew_RejectsInvalidRules(t *testing.T) {
	dupTeam := testRules()
	dupTeam.Teams = append(dupTeam.Teams, dupTeam.Teams[0])
	sharedAlias := testRules()
	sharedAlias.Teams[1].Aliases = append(sharedAlias.Teams[1].Aliases, "Herons")
	badCorrection := testRules()
	badCorrection.Corrections = []leaguegazetteer.Correction{{Mention: "Foo", GSISID: "g-missing"}}
	emptyName := testRules()
	emptyName.Teams[0].Name = ""
	punctuationName := testRules()
	punctuationName.Teams[0].Name = "!!!"
	for name, rules := range map[string]leaguegazetteer.Rules{
		"duplicate team abbr": dupTeam, "alias claimed by two teams": sharedAlias,
		"correction to unknown player": badCorrection, "team without a name": emptyName,
		"team name that normalises to nothing": punctuationName,
	} {
		if _, err := leaguegazetteer.New(testPlayers(), rules); err == nil {
			t.Errorf("%s: New() error = nil, want an error", name)
		}
	}
}

func TestNormalizeText_JoinsAndNormalises(t *testing.T) {
	if got := leaguegazetteer.NormalizeText("Herons BEAT", "the Capitals!"); got != strings.Join([]string{"herons", "beat", "the", "capitals"}, " ") {
		t.Fatalf("NormalizeText() = %q", got)
	}
}

func TestCorroboration_DoesNotUseDerivedSingularNicknames(t *testing.T) {
	idx, err := leaguegazetteer.New(append(testPlayers(), p("Josh Wrenfield", "Josh", "Wrenfield", "HC", "QB", "ACT", "g-jwrenfield")), leaguegazetteer.Rules{
		Teams: []leaguegazetteer.TeamRule{{Abbr: "HC", Name: "Harbor City 88ers", Aliases: []string{"eighters"}}, {Abbr: "SHE", Name: "Shelbyville Sharks"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrenfield, _ := idx.PlayerByGSIS("g-jwrenfield")
	if idx.Corroborated(wrenfield, leaguegazetteer.Context{Text: leaguegazetteer.NormalizeText("the 88er said wrenfield")}) {
		t.Error("the derived singular '88er' must not corroborate")
	}
	if !idx.Corroborated(wrenfield, leaguegazetteer.Context{Text: leaguegazetteer.NormalizeText("the eighters and wrenfield")}) {
		t.Error("an explicit alias must still corroborate")
	}
	// ...but the singular still resolves a TEAM mention (phase 1 behaviour).
	if got := idx.ResolveTeam("88er"); got.Status != leaguegazetteer.TeamResolved {
		t.Errorf("ResolveTeam(88er) = %+v, want resolved", got)
	}
}
