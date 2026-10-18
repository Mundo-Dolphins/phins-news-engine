package classification

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

func TestApplyAnchor(t *testing.T) {
	policy := AnchorPolicy{
		Enabled:             true,
		Categories:          map[string]bool{"official_names": true, "active_roster_core": true, "team_hashtags": true},
		MultiWordCategories: map[string]bool{"active_roster_core": true},
	}
	related := func(ev ...Evidence) Result {
		return Result{Related: true, RuleApplied: RulePositiveHigh, PositiveEvidence: ev}
	}
	tests := []struct {
		name string
		in   Result
		p    AnchorPolicy
		want bool
	}{
		{"disabled keeps result", related(Evidence{Category: "venue", EntryName: "Hard Rock Stadium"}), AnchorPolicy{}, true},
		{"venue only is demoted", related(Evidence{Category: "venue", EntryName: "Hard Rock Stadium"}), policy, false},
		{"bare surname is demoted", related(Evidence{Category: "active_roster_core", EntryName: "jones"}), policy, false},
		{"full name anchors", related(Evidence{Category: "active_roster_core", EntryName: "Bart Simpson"}), policy, true},
		{"official name anchors", related(Evidence{Category: "official_names", EntryName: "Springfield Isotopes"}), policy, true},
		{"unrelated untouched", Result{RuleApplied: RuleBelowThreshold}, policy, false},
	}
	for _, tc := range tests {
		got := ApplyAnchor(tc.in, tc.p, now)
		if got.Related != tc.want {
			t.Errorf("%s: Related = %v, want %v", tc.name, got.Related, tc.want)
		}
	}
	weak := AnchorPolicy{Enabled: true, Categories: map[string]bool{"active_roster_core": true}, MinConfidence: 0.7}
	past := now.AddDate(0, 0, -10)
	for name, ev := range map[string]Evidence{
		"low confidence": {Category: "active_roster_core", EntryName: "Kirk Cousins", Confidence: 0.5},
		"retired entry":  {Category: "active_roster_core", EntryName: "Homer Simpson", Confidence: 0.9, ActiveUntil: &past},
	} {
		if ApplyAnchor(related(ev), weak, now).Related {
			t.Errorf("%s must not anchor", name)
		}
	}
	if !ApplyAnchor(related(Evidence{Category: "active_roster_core", EntryName: "Bart Simpson", Confidence: 0.8}), weak, now).Related {
		t.Error("confident active entry must anchor")
	}
	if got := ApplyAnchor(related(Evidence{Category: "venue", EntryName: "x"}), policy, now); got.RuleApplied != RuleNoAnchor {
		t.Errorf("RuleApplied = %q, want %q", got.RuleApplied, RuleNoAnchor)
	}
}
