package reviewdecision_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
)

func TestResolve_MapsEveryKnownAction(t *testing.T) {
	tests := []struct {
		action reviewdecision.Action
		want   reviewdecision.Outcome
	}{
		{reviewdecision.ActionPublish, reviewdecision.Outcome{Label: "publish", Reason: "", Persist: true}},
		{reviewdecision.ActionReject, reviewdecision.Outcome{Label: "reject", Reason: "quality", Persist: true}},
		{reviewdecision.ActionDuplicate, reviewdecision.Outcome{Label: "reject", Reason: "duplicate", Persist: true}},
		{reviewdecision.ActionSameNews, reviewdecision.Outcome{Label: "reject", Reason: "same_event", Persist: true}},
		{reviewdecision.ActionUpdate, reviewdecision.Outcome{Label: "publish", Reason: "substantial_update", Persist: true}},
		{reviewdecision.ActionAmbiguous, reviewdecision.Outcome{}},
		{reviewdecision.ActionEditCategory, reviewdecision.Outcome{}},
		{reviewdecision.ActionSkip, reviewdecision.Outcome{}},
	}
	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			got, err := reviewdecision.Resolve(tt.action)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tt.action, err)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %+v, want %+v", tt.action, got, tt.want)
			}
		})
	}
}

func TestResolve_RejectsUnknownAction(t *testing.T) {
	if _, err := reviewdecision.Resolve(reviewdecision.Action("bogus")); err == nil {
		t.Error("Resolve(\"bogus\") error = nil, want an error for an unknown action")
	}
}

func TestAction_Valid(t *testing.T) {
	valid := []reviewdecision.Action{
		reviewdecision.ActionPublish, reviewdecision.ActionReject, reviewdecision.ActionAmbiguous,
		reviewdecision.ActionDuplicate, reviewdecision.ActionSameNews, reviewdecision.ActionUpdate,
		reviewdecision.ActionEditCategory, reviewdecision.ActionSkip,
	}
	for _, a := range valid {
		if !a.Valid() {
			t.Errorf("%q.Valid() = false, want true", a)
		}
	}
	if reviewdecision.Action("bogus").Valid() {
		t.Error("\"bogus\".Valid() = true, want false")
	}
}
