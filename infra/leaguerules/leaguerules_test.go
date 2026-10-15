// infra/leaguerules/leaguerules_test.go
package leaguerules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguerules"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rules.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_ParsesAllSections(t *testing.T) {
	rules, err := leaguerules.Load(write(t, `
noise_min_letters: 4
common_words: [all, dial]
teams:
  - abbr: SPR
    name: Springfield Isotopes
    aliases: [isotopes, topes]
    upper_abbrs: [SPR]
corrections:
  - mention: Big Burns
    gsis_id: g-1
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if rules.NoiseMinLetters != 4 || len(rules.CommonWords) != 2 || len(rules.Teams) != 1 || rules.Teams[0].UpperAbbrs[0] != "SPR" ||
		len(rules.Corrections) != 1 || rules.Corrections[0].GSISID != "g-1" {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestLoad_RejectsUnknownFieldsAndMissingFiles(t *testing.T) {
	if _, err := leaguerules.Load(write(t, "teams: []\nsurprise: 1\n")); err == nil {
		t.Error("an unknown top-level field must be an error")
	}
	if _, err := leaguerules.Load(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Error("a missing file must be an error")
	}
	if _, err := leaguerules.Load(write(t, "teams: [")); err == nil {
		t.Error("malformed YAML must be an error")
	}
}

// TestExampleRulesFile_BuildsAWorkingIndex loads the shipped example rules
// (a fictional six-team league) and checks the resolution rules the
// gazetteer applies to them: aliases, upper-case abbreviations, singular
// nicknames, compound and truncated informal forms that must not be
// rejected, and the closed-set rejection of names outside the league.
func TestExampleRulesFile_BuildsAWorkingIndex(t *testing.T) {
	rules, err := leaguerules.Load(repofs.ExampleConfig(t, "league_alias_rules.yml"))
	if err != nil {
		t.Fatalf("Load(example rules) error = %v", err)
	}
	if len(rules.Teams) != 6 {
		t.Fatalf("teams = %d, want 6", len(rules.Teams))
	}
	idx, err := leaguegazetteer.New(nil, rules)
	if err != nil {
		t.Fatalf("New(example rules) error = %v", err)
	}
	for mention, abbr := range map[string]string{"Topes": "SPR", "Isotopes": "SPR", "Caps": "CAP", "Sharks": "SHE", "SPF": "SPR", "SHV": "SHE", "OGD": "OGD", "Springfield Isotopes": "SPR", "Shark": "SHE"} {
		if got := idx.ResolveTeam(mention); got.Abbr != abbr {
			t.Errorf("ResolveTeam(%q) = %+v, want %s", mention, got, abbr)
		}
	}
	// Informal, truncated or compound forms of a league team must not be rejected.
	for _, mention := range []string{"SpringfieldIsotopes", "Ogdenvil", "Springfield", "Example League"} {
		if got := idx.ResolveTeam(mention); got.Status == leaguegazetteer.TeamNotNFL {
			t.Errorf("ResolveTeam(%q) = %+v, an informal, truncated or compound league form must not be rejected", mention, got)
		}
	}
	// Abbreviations in any case, including the two-letter one, are league
	// vocabulary and must never be rejected (restored from the old test's
	// abbreviation-collision cases, on the synthetic teams).
	for _, mention := range []string{"NH", "Nh", "nh", "SPR", "spr", "Spr", "NHV", "Cap", "brk"} {
		if got := idx.ResolveTeam(mention); got.Status == leaguegazetteer.TeamNotNFL {
			t.Errorf("ResolveTeam(%q) = %+v, an abbreviation in any case must never be rejected", mention, got)
		}
	}
	// Names outside the closed set stay rejected.
	for _, mention := range []string{"Central State", "Glimmerfish", "Example College", "Duskhounds"} {
		if got := idx.ResolveTeam(mention); got.Status != leaguegazetteer.TeamNotNFL {
			t.Errorf("ResolveTeam(%q) = %+v, want NotNFL", mention, got)
		}
	}
}
