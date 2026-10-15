// app/leaguegazetteer/export_test.go
package leaguegazetteer_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func exported(t *testing.T, taken map[string]bool) map[string]eventsignals.EntityRule {
	t.Helper()
	idx := newIndex(t)
	teamIDs := map[string]string{"WAV": "waverly-herons", "CAP": "capital-city-capitals", "COB": "cobbleton-ocelots", "CYP": "cypress-comets", "MES": "mesa-condors", "SPR": "springfield-isotopes", "HC": "harbor-city-88ers", "LKV": "lakeview-foals", "LA": "coastline-rhinos"}
	out := map[string]eventsignals.EntityRule{}
	for _, r := range idx.ExportRules(teamIDs, taken) {
		out[r.CanonicalName+"|"+r.ID] = r
	}
	return out
}

func find(rules map[string]eventsignals.EntityRule, canonical string) (eventsignals.EntityRule, bool) {
	for k, r := range rules {
		if strings.HasPrefix(k, canonical+"|") {
			return r, true
		}
	}
	return eventsignals.EntityRule{}, false
}

func TestExportRules_BuildsLeagueScopePlayerRules(t *testing.T) {
	rules := exported(t, nil)
	burns, ok := find(rules, "Dorian Burns")
	if !ok {
		t.Fatal("Dorian Burns was not exported")
	}
	if burns.Scope != eventsignals.ScopeLeague || burns.Kind != eventsignals.EntityKindPlayer || !strings.HasPrefix(burns.ID, "league-dorian-burns") {
		t.Fatalf("rule = %+v", burns)
	}
	if burns.TeamID != "waverly-herons" || burns.AffiliationStatus != eventsignals.AffiliationTeam || burns.Position != "QB" {
		t.Fatalf("affiliation fields = %+v", burns)
	}
	if len(burns.Terms) == 0 || burns.Terms[0] != "Dorian Burns" {
		t.Fatalf("Terms = %v, want the raw full name first", burns.Terms)
	}
	if len(burns.ConditionalTerms) != 1 || burns.ConditionalTerms[0].Term != "Burns" {
		t.Fatalf("ConditionalTerms = %+v, want the unique surname", burns.ConditionalTerms)
	}
	for _, ctx := range burns.ConditionalTerms[0].RequireAny {
		if ctx == "heron" {
			t.Errorf("RequireAny %v contains a derived singular", burns.ConditionalTerms[0].RequireAny)
		}
	}
	if !strings.Contains(strings.Join(burns.ConditionalTerms[0].RequireAny, ","), "herons") {
		t.Fatalf("RequireAny = %v, want the team nickname", burns.ConditionalTerms[0].RequireAny)
	}
	if len(burns.Prefilter) == 0 {
		t.Fatal("Prefilter must be populated")
	}
}

func TestExportRules_SharedAndStopWordSurnamesGetNoConditionalAlias(t *testing.T) {
	rules := exported(t, nil)
	for _, name := range []string{"Zorin Brackwater", "Teague Brackwater", "Osric Brackwater"} { // shared surname
		if r, ok := find(rules, name); !ok || len(r.ConditionalTerms) != 0 {
			t.Errorf("%s: exported=%v conditional=%v; a shared surname must not be an alias", name, ok, r.ConditionalTerms)
		}
	}
	if r, ok := find(rules, "Fennick All"); !ok || len(r.ConditionalTerms) != 0 { // "all" is a configured common word
		t.Errorf("Fennick All: exported=%v conditional=%v; a stop-word surname must not be an alias", ok, r.ConditionalTerms)
	}
}

func TestExportRules_HomonymsGetDistinctIds(t *testing.T) {
	rules := exported(t, nil)
	ids := map[string]bool{}
	n := 0
	for k, r := range rules {
		if strings.HasPrefix(k, "Jaylen Smith|") {
			n++
			ids[r.ID] = true
		}
	}
	if n != 2 || len(ids) != 2 {
		t.Fatalf("homonyms exported %d with %d distinct ids, want 2 and 2", n, len(ids))
	}
}

func TestExportRules_SkipsPlayersThatCollideWithTheIsotopesCatalog(t *testing.T) {
	rules := exported(t, map[string]bool{"tom-brandt": true})
	if _, ok := find(rules, "Tom Brandt"); ok {
		t.Error("a player whose slug is already a Isotopes entity must be skipped")
	}
	if _, ok := find(rules, "Dorian Burns"); !ok {
		t.Error("an unrelated player must still be exported")
	}
}

func TestIdentityKey_IgnoresSuffixesAccentsAndCase(t *testing.T) {
	a := leaguegazetteer.IdentityKey("Ollie Penhallow II")
	for _, same := range []string{"Ollie Penhallow", "ollie  penhallow", "Ollie Penhallow Jr."} {
		if got := leaguegazetteer.IdentityKey(same); got != a {
			t.Errorf("IdentityKey(%q) = %q, want %q", same, got, a)
		}
	}
	if leaguegazetteer.IdentityKey("Ollie Penhallow") == leaguegazetteer.IdentityKey("Ollie Penhallow Smith") {
		t.Error("a different surname must give a different key")
	}
}
