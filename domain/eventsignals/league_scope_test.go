// domain/eventsignals/league_scope_test.go
package eventsignals_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func baseRules() eventsignals.Rules {
	return eventsignals.Rules{
		Version:      "test",
		FieldWeights: map[string]float64{"title": 1, "description": 0.8, "body": 0.4},
		ActionTerms:  map[eventsignals.Action][]string{"sign": {"signs", "signed"}},
		Entities: []eventsignals.EntityRule{
			{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"isotopes", "springfield isotopes"}, IsPrimaryTeam: true},
			{ID: "shelbyville-sharks", CanonicalName: "Shelbyville Sharks", Kind: eventsignals.EntityKindTeam, Terms: []string{"sharks", "shelbyville sharks"}},
			{ID: "ezo-quarrington", CanonicalName: "Ezo Quarrington", Kind: eventsignals.EntityKindPlayer, Terms: []string{"ezo quarrington", "quarrington", "ezo"}},
		},
	}
}

func leagueEntities() []eventsignals.EntityRule {
	return []eventsignals.EntityRule{
		{
			ID: "league-josh-wrenfield", CanonicalName: "Josh Wrenfield", Kind: eventsignals.EntityKindPlayer, Scope: eventsignals.ScopeLeague,
			Terms:     []string{"josh wrenfield"},
			Prefilter: eventsignals.PrefilterWords("josh wrenfield"),
		},
		{
			ID: "league-dorian-burns", CanonicalName: "Dorian Burns", Kind: eventsignals.EntityKindPlayer, Scope: eventsignals.ScopeLeague,
			Terms:            []string{"dorian burns"},
			ConditionalTerms: []eventsignals.ConditionalTerm{{Term: "burns", RequireAny: []string{"herons", "birds"}}},
			Prefilter:        eventsignals.PrefilterWords("dorian burns", "burns"),
		},
	}
}

func withLeague(r eventsignals.Rules) eventsignals.Rules {
	r.Entities = append(append([]eventsignals.EntityRule(nil), r.Entities...), leagueEntities()...)
	return r
}

func art(id, title, summary, body string) article.Article {
	return article.Article{ID: id, Title: title, Summary: summary, Body: body, PublishedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
}

var corpus = []article.Article{
	art("a1", "Josh Wrenfield leads Sharks past Isotopes as Ezo Quarrington struggles", "Shelbyville wins at home.", ""),
	art("a2", "Isotopes sign a free agent", "Springfield signs depth.", "Ezo will start Sunday. Josh Wrenfield watched."),
	art("a3", "Burns and the Herons beat Springfield", "Dorian Burns threw two touchdowns against the Isotopes.", ""),
	art("a4", "It burns to lose", "The Isotopes fell again.", ""),
	art("a5", "League news", "Nothing about any roster.", "Plain text about the weather."),
	art("a6", "Sharks at Isotopes preview", "Wrenfield and Ezo meet.", "Josh Wrenfield and Dorian Burns are mentioned in the body only."),
}

// zeroAllowedDifferences blanks the three fields league rules may change.
func zeroAllowedDifferences(r eventsignals.Representation) eventsignals.Representation {
	r.Entities, r.EntityMentions, r.UnknownEntities = nil, nil, nil
	return r
}

func TestLeagueScope_NeverChangesAnythingOutsideTheThreeAllowedFields(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, a := range corpus {
		off := eventsignals.Extract(a, baseRules(), now)
		on := eventsignals.Extract(a, withLeague(baseRules()), now)
		if !reflect.DeepEqual(zeroAllowedDifferences(off), zeroAllowedDifferences(on)) {
			t.Errorf("article %s: league rules changed more than Entities/EntityMentions/UnknownEntities\n off: %+v\n  on: %+v", a.ID, zeroAllowedDifferences(off), zeroAllowedDifferences(on))
		}
	}
}

func TestLeagueScope_MentionIsRecordedButNeverPrimary(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	rep := eventsignals.Extract(corpus[0], withLeague(baseRules()), now)
	if rep.PrimaryEntity != "Ezo Quarrington" {
		t.Fatalf("PrimaryEntity = %q; Josh Wrenfield is mentioned first but is league scope and must not displace the Isotopes player", rep.PrimaryEntity)
	}
	var found bool
	for _, m := range rep.EntityMentions {
		if m.ID == "league-josh-wrenfield" {
			found = true
			if m.Scope != eventsignals.ScopeLeague || m.Field != "title" {
				t.Errorf("mention = %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("EntityMentions = %+v; the league player must be recorded", rep.EntityMentions)
	}
	hasName := false
	for _, e := range rep.Entities {
		hasName = hasName || e == "Josh Wrenfield"
	}
	if !hasName {
		t.Fatalf("Entities = %v; want Josh Wrenfield listed", rep.Entities)
	}
	for _, e := range append(append([]string(nil), rep.PrimaryEntities...), rep.SecondaryEntities...) {
		if e == "league-josh-wrenfield" {
			t.Fatalf("league entity leaked into primary/secondary entities: %+v %+v", rep.PrimaryEntities, rep.SecondaryEntities)
		}
	}
}

func TestLeagueScope_EntitiesStayGloballyOrderedByFirstMention(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	off := eventsignals.Extract(corpus[0], baseRules(), now)
	on := eventsignals.Extract(corpus[0], withLeague(baseRules()), now)

	// Title order: Josh Wrenfield, Sharks, Isotopes, Ezo Quarrington.
	want := []string{"Josh Wrenfield", "Shelbyville Sharks", "Springfield Isotopes", "Ezo Quarrington"}
	if !reflect.DeepEqual(on.Entities, want) {
		t.Fatalf("Entities = %v, want %v (first-mention order across both scopes)", on.Entities, want)
	}
	var isotopesOnly []eventsignals.EntityMention
	for _, m := range on.EntityMentions {
		if m.Scope == "" {
			isotopesOnly = append(isotopesOnly, m)
		}
	}
	if !reflect.DeepEqual(off.EntityMentions, isotopesOnly) {
		t.Fatalf("the Isotopes-scope mentions must be unchanged and in the same relative order:\n off: %+v\n  on: %+v", off.EntityMentions, isotopesOnly)
	}
	for _, m := range off.EntityMentions {
		if m.Scope != "" {
			t.Errorf("a Isotopes-scope mention must have an empty Scope, got %+v", m)
		}
	}
}

func TestConditionalTerm_NeedsItsContext(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	has := func(rep eventsignals.Representation, id string) bool {
		for _, m := range rep.EntityMentions {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	// "Burns" + "Herons": matches through the conditional surname.
	if rep := eventsignals.Extract(art("x", "Burns and the Herons win", "", ""), withLeague(baseRules()), now); !has(rep, "league-dorian-burns") {
		t.Fatalf("EntityMentions = %+v; surname plus team context must match", rep.EntityMentions)
	}
	// "burns" as a verb, no team context: no match.
	if rep := eventsignals.Extract(art("y", "It burns to lose", "The Isotopes fell", ""), withLeague(baseRules()), now); has(rep, "league-dorian-burns") {
		t.Fatalf("EntityMentions = %+v; the surname without its team context must not match", rep.EntityMentions)
	}
	// The unconditional full name needs no context.
	if rep := eventsignals.Extract(art("z", "Dorian Burns is injured", "", ""), withLeague(baseRules()), now); !has(rep, "league-dorian-burns") {
		t.Fatalf("EntityMentions = %+v; the full name must match on its own", rep.EntityMentions)
	}
}

func TestPrefilter_IsOnlyAnOptimisation(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	noFilter := withLeague(baseRules())
	for i := range noFilter.Entities {
		noFilter.Entities[i].Prefilter = nil
	}
	for _, a := range corpus {
		if got, want := eventsignals.Extract(a, withLeague(baseRules()), now), eventsignals.Extract(a, noFilter, now); !reflect.DeepEqual(got, want) {
			t.Errorf("article %s: the prefilter changed the result\n with: %+v\n none: %+v", a.ID, got, want)
		}
	}
}

func TestPrefilterWords_FirstWordOfEachTermLowerCased(t *testing.T) {
	got := eventsignals.PrefilterWords("Josh Wrenfield", "Ja'Ron Quillfeather", "88ers", "  ", "Burns")
	want := []string{"josh", "ja", "88ers", "burns"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PrefilterWords() = %v, want %v", got, want)
	}
}

func TestUnknownEntities_LeagueNamesAreKnown(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	a := art("u", "Josh Wrenfield visits Springfield", "Dorian Burns also spoke", "")
	if rep := eventsignals.Extract(a, baseRules(), now); len(rep.UnknownEntities) == 0 {
		t.Fatal("fixture produces no unknown entities without league rules; the assertion below would be vacuous — change the article")
	}
	if rep := eventsignals.Extract(a, withLeague(baseRules()), now); len(rep.UnknownEntities) != 0 {
		t.Fatalf("UnknownEntities = %+v; names owned by league rules are known", rep.UnknownEntities)
	}
}
