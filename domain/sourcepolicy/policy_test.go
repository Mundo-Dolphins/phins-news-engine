package sourcepolicy_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

func TestAuthorityLevel_Valid(t *testing.T) {
	valid := []sourcepolicy.AuthorityLevel{
		sourcepolicy.AuthorityOfficialTeam, sourcepolicy.AuthorityOfficialLeague,
		sourcepolicy.AuthorityOfficialPlayer, sourcepolicy.AuthorityOfficialStaff,
		sourcepolicy.AuthorityPrimaryReporter, sourcepolicy.AuthorityTrustedReporter,
		sourcepolicy.AuthorityRegularSource, sourcepolicy.AuthorityAggregator,
		sourcepolicy.AuthorityUnknown,
	}
	for _, a := range valid {
		if !a.Valid() {
			t.Errorf("AuthorityLevel(%q).Valid() = false, want true", a)
		}
	}
	if sourcepolicy.AuthorityLevel("made_up").Valid() {
		t.Error("AuthorityLevel(\"made_up\").Valid() = true, want false")
	}
}

func TestRepresentativePreference_Valid(t *testing.T) {
	for _, p := range []sourcepolicy.RepresentativePreference{
		sourcepolicy.PreferenceNone, sourcepolicy.PreferenceLow,
		sourcepolicy.PreferenceMedium, sourcepolicy.PreferenceHigh,
	} {
		if !p.Valid() {
			t.Errorf("RepresentativePreference(%q).Valid() = false, want true", p)
		}
	}
	if sourcepolicy.RepresentativePreference("extreme").Valid() {
		t.Error("RepresentativePreference(\"extreme\").Valid() = true, want false")
	}
}

func TestMode_Valid(t *testing.T) {
	for _, m := range []sourcepolicy.Mode{sourcepolicy.ModeDisabled, sourcepolicy.ModeShadow, sourcepolicy.ModeEnforce} {
		if !m.Valid() {
			t.Errorf("Mode(%q).Valid() = false, want true", m)
		}
	}
	if sourcepolicy.Mode("full-send").Valid() {
		t.Error("Mode(\"full-send\").Valid() = true, want false")
	}
}

func TestDefaultPolicy_IsFullySafe(t *testing.T) {
	p := sourcepolicy.DefaultPolicy("some-source")
	if p.SourceID != "some-source" {
		t.Errorf("SourceID = %q, want %q", p.SourceID, "some-source")
	}
	if p.Authority != sourcepolicy.AuthorityUnknown {
		t.Errorf("Authority = %q, want %q", p.Authority, sourcepolicy.AuthorityUnknown)
	}
	if p.RepresentativePreference != sourcepolicy.PreferenceNone {
		t.Errorf("RepresentativePreference = %q, want %q", p.RepresentativePreference, sourcepolicy.PreferenceNone)
	}
	if p.RetainNearDuplicates || p.AllowOfficialConfirmation || p.AllowSubstantialUpdate || p.PublishConfirmationWithoutNewInformation {
		t.Error("DefaultPolicy must have every bool false")
	}
	if p.MinimumUpdateNovelty != 0 || p.EventCooldown != 0 {
		t.Error("DefaultPolicy must have zero novelty threshold and zero cooldown")
	}
}

func TestPolicy_PrefersAsRepresentative(t *testing.T) {
	cases := []struct {
		pref sourcepolicy.RepresentativePreference
		want bool
	}{
		{sourcepolicy.PreferenceNone, false},
		{sourcepolicy.PreferenceLow, false},
		{sourcepolicy.PreferenceMedium, true},
		{sourcepolicy.PreferenceHigh, true},
	}
	for _, c := range cases {
		p := sourcepolicy.Policy{RepresentativePreference: c.pref}
		if got := p.PrefersAsRepresentative(); got != c.want {
			t.Errorf("Policy{RepresentativePreference: %q}.PrefersAsRepresentative() = %v, want %v", c.pref, got, c.want)
		}
	}
}
