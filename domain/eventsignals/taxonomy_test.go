// domain/eventsignals/taxonomy_test.go
package eventsignals_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals/eventsignalstest"
)

func TestTaxonomy_Classify(t *testing.T) {
	tax := eventsignalstest.Taxonomy()
	cases := []struct {
		action eventsignals.Action
		signal eventsignals.ContentSignal
		want   eventsignals.EventType
	}{
		{"sign", "", eventsignals.EventTypeTransaction},
		{"sign", "rumor", eventsignals.EventTypeRumor},
		{"sign", "analysis", eventsignals.EventTypeTransaction},
		{"", "analysis", eventsignals.EventTypeAnalysis},
		{"", "", eventsignals.EventTypeGeneric},
		{"unknown", "", eventsignals.EventTypeGeneric},
	}
	for _, c := range cases {
		if got := tax.Classify(c.action, c.signal); got != c.want {
			t.Errorf("Classify(%q, %q) = %q, want %q", c.action, c.signal, got, c.want)
		}
	}
}

func TestTaxonomy_Priorities(t *testing.T) {
	tax := eventsignalstest.Taxonomy()
	wantBody := []eventsignals.Action{
		"injured-reserve", "practice-squad", "promotion", "demotion", "activate", "waive",
		"claim", "release", "trade", "sign", "roster", "injure",
	}
	if got := tax.BodyFallbackPriority(); !reflect.DeepEqual(got, wantBody) {
		t.Errorf("BodyFallbackPriority = %v, want %v", got, wantBody)
	}
	if got := tax.ActionPriority()[0]; got != "practice-squad" {
		t.Errorf("ActionPriority()[0] = %q, want practice-squad", got)
	}
	wantSignals := []eventsignals.ContentSignal{"correction", "confirmation", "rumor", "analysis", "reaction", "quote"}
	if got := tax.SignalPriority(); !reflect.DeepEqual(got, wantSignals) {
		t.Errorf("SignalPriority = %v, want %v", got, wantSignals)
	}
}

func TestTaxonomy_ZeroValueIsGeneric(t *testing.T) {
	if got := (eventsignals.Taxonomy{}).Classify("sign", "rumor"); got != eventsignals.EventTypeGeneric {
		t.Errorf("zero Taxonomy Classify = %q, want generic", got)
	}
}

func TestTaxonomy_Lookups(t *testing.T) {
	tax := eventsignalstest.Taxonomy()
	wantNames := []eventsignals.EventType{
		"transaction", "injury", "draft", "game", "practice", "quote", "rumor",
		"confirmation", "correction", "analysis", "reaction", "generic",
	}
	if got := tax.EventTypeNames(); !reflect.DeepEqual(got, wantNames) {
		t.Errorf("EventTypeNames = %v, want %v", got, wantNames)
	}
	if !tax.HasFlag("correction", eventsignals.FlagUpdateFraming) || !tax.HasFlag("correction", eventsignals.FlagReferential) {
		t.Error("correction should be referential and update_framing")
	}
	if tax.HasFlag("transaction", eventsignals.FlagReferential) || tax.HasFlag("nope", eventsignals.FlagReferential) {
		t.Error("transaction/unknown should not be referential")
	}
	if !tax.HasFlag("game", eventsignals.FlagTeamLevelNeedsOpponent) {
		t.Error("game should need an opponent at team level")
	}
	if !tax.SignalHasFlag("rumor", eventsignals.FlagSpeculative) || tax.SignalHasFlag("analysis", eventsignals.FlagSpeculative) {
		t.Error("only rumor is speculative")
	}
	if got := tax.SignalEventType("confirmation"); got != eventsignals.EventTypeConfirmation {
		t.Errorf("SignalEventType(confirmation) = %q", got)
	}
	if got := tax.SignalEventType("nope"); got != "" {
		t.Errorf("SignalEventType(nope) = %q, want empty", got)
	}
	if got := tax.MemberRole("reaction"); got != "reaction" {
		t.Errorf("MemberRole(reaction) = %q", got)
	}
	if got := tax.MemberRole("transaction"); got != "" {
		t.Errorf("MemberRole(transaction) = %q, want empty", got)
	}
	if got := tax.Description("draft"); got != "A draft pick or draft-related news." {
		t.Errorf("Description(draft) = %q", got)
	}
}

func TestTaxonomyHelper_FreshValue(t *testing.T) {
	a := eventsignalstest.Taxonomy()
	a.EventTypes[0].Name = "mutated"
	a.EventTypes[3].Flags[0] = "mutated"
	b := eventsignalstest.Taxonomy()
	if b.EventTypes[0].Name != "transaction" || b.EventTypes[3].Flags[0] != eventsignals.FlagTeamLevelNeedsOpponent {
		t.Error("eventsignalstest.Taxonomy returned shared state")
	}
}
