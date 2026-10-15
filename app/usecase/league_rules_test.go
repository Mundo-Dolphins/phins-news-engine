// app/usecase/league_rules_test.go
package usecase

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

type fakeSource struct {
	entities  []eventsignals.EntityRule
	refreshes int
}

func (f *fakeSource) Refresh(time.Time)                   { f.refreshes++ }
func (f *fakeSource) Entities() []eventsignals.EntityRule { return f.entities }

func baseWithOneEntity() eventsignals.Rules {
	return eventsignals.Rules{Entities: []eventsignals.EntityRule{{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"isotopes"}}}}
}

func leagueRule(id string) eventsignals.EntityRule {
	return eventsignals.EntityRule{ID: id, CanonicalName: id, Kind: eventsignals.EntityKindPlayer, Scope: eventsignals.ScopeLeague, Terms: []string{id}}
}

func TestLeagueRulesCache_NilOrEmptySourceReturnsTheBaseUnchanged(t *testing.T) {
	var c leagueRulesCache
	base := baseWithOneEntity()
	if got := c.rulesFor(base, nil); len(got.Entities) != 1 {
		t.Fatalf("nil source: %d entities", len(got.Entities))
	}
	if got := c.rulesFor(base, &fakeSource{}); len(got.Entities) != 1 {
		t.Fatalf("empty source: %d entities", len(got.Entities))
	}
}

func TestLeagueRulesCache_MergesWithoutMutatingTheSharedBackingArray(t *testing.T) {
	var c leagueRulesCache
	base := baseWithOneEntity()
	// Give the base spare capacity: a careless append would write into it.
	base.Entities = append(make([]eventsignals.EntityRule, 0, 10), base.Entities...)
	src := &fakeSource{entities: []eventsignals.EntityRule{leagueRule("league-a"), leagueRule("league-b")}}
	got := c.rulesFor(base, src)
	if len(got.Entities) != 3 || got.Entities[0].ID != "springfield-isotopes" || got.Entities[2].ID != "league-b" {
		t.Fatalf("merged = %+v", got.Entities)
	}
	if len(base.Entities) != 1 || base.Entities[:2][1].ID != "" {
		t.Fatalf("the base slice or its spare capacity was modified: len=%d second=%+v", len(base.Entities), base.Entities[:2][1])
	}
}

func TestLeagueRulesCache_ReusesTheMergeUntilTheSourceSliceChanges(t *testing.T) {
	var c leagueRulesCache
	base := baseWithOneEntity()
	src := &fakeSource{entities: []eventsignals.EntityRule{leagueRule("league-a")}}
	first := c.rulesFor(base, src)
	again := c.rulesFor(base, src)
	if &first.Entities[0] != &again.Entities[0] {
		t.Fatal("an unchanged source must reuse the merged slice instead of rebuilding it for every article")
	}
	src.entities = []eventsignals.EntityRule{leagueRule("league-a"), leagueRule("league-c")}
	if got := c.rulesFor(base, src); len(got.Entities) != 3 {
		t.Fatalf("after the source changed: %d entities, want 3", len(got.Entities))
	}
	src.entities = nil
	if got := c.rulesFor(base, src); len(got.Entities) != 1 {
		t.Fatalf("after the source emptied: %d entities, want the base only", len(got.Entities))
	}
}
