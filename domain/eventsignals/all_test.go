// domain/eventsignals/all_test.go
package eventsignals_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func TestAllActions_ClosedListWithoutDuplicates(t *testing.T) {
	got := eventsignals.AllActions()
	if len(got) != 26 {
		t.Fatalf("len = %d, want 26: %v", len(got), got)
	}
	seen := map[eventsignals.Action]bool{}
	for _, a := range got {
		if a == "" || seen[a] {
			t.Fatalf("empty or duplicate action %q", a)
		}
		seen[a] = true
	}
	for _, want := range []eventsignals.Action{eventsignals.ActionSign, eventsignals.ActionCorrect, eventsignals.ActionRoster, eventsignals.ActionInjuredReserve} {
		if !seen[want] {
			t.Errorf("missing %q", want)
		}
	}
	got[0] = "mutated"
	if eventsignals.AllActions()[0] == "mutated" {
		t.Error("AllActions returned shared backing array")
	}
}

func TestAllContentSignals_ClosedList(t *testing.T) {
	got := eventsignals.AllContentSignals()
	want := map[eventsignals.ContentSignal]bool{
		eventsignals.ContentSignalRumor: true, eventsignals.ContentSignalConfirmation: true,
		eventsignals.ContentSignalCorrection: true, eventsignals.ContentSignalAnalysis: true,
		eventsignals.ContentSignalReaction: true, eventsignals.ContentSignalQuote: true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected %q", s)
		}
		delete(want, s)
	}
	got[0] = "mutated"
	if eventsignals.AllContentSignals()[0] == "mutated" {
		t.Error("AllContentSignals returned shared backing array")
	}
}

func TestAllEventTypes_FreshCopyGenericLast(t *testing.T) {
	got := eventsignals.AllEventTypes()
	if len(got) != 12 {
		t.Fatalf("len = %d, want 12: %v", len(got), got)
	}
	if last := got[len(got)-1]; last != eventsignals.EventTypeGeneric {
		t.Errorf("last = %q, want %q", last, eventsignals.EventTypeGeneric)
	}
	got[0] = "mutated"
	if eventsignals.AllEventTypes()[0] == "mutated" {
		t.Error("AllEventTypes returned shared backing array")
	}
}
