package dictionary_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func validDecision() dictionary.Decision {
	return dictionary.Decision{
		PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, NegativeThresholdHigh: 0,
		TitleWeightMultiplier: 2, MaxCountPerTerm: 3,
	}
}

func TestValidate_ValidDictionaryHasNoErrors(t *testing.T) {
	d := dictionary.Dictionary{
		Version: "1.0.0",
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{
				{CanonicalName: "Springfield Isotopes", Source: "manual", Confidence: 1},
			}},
			{Name: "ambiguous_team_terms", Weight: 4, RequiresAnyOf: []string{"official_names"}, Entries: []dictionary.Entry{
				{CanonicalName: "isotopes", Source: "manual", Confidence: 0.8},
			}},
		},
		Decision: validDecision(),
	}
	issues := dictionary.Validate(d)
	for _, issue := range issues {
		if issue.Severity == dictionary.SeverityError {
			t.Errorf("unexpected error issue: %v", issue)
		}
	}
}

func TestValidate_DuplicateCanonicalNameAcrossCategoriesIsError(t *testing.T) {
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{
			{Name: "a", Entries: []dictionary.Entry{{CanonicalName: "Ezo Quarrington", Source: "x"}}},
			{Name: "b", Entries: []dictionary.Entry{{CanonicalName: "ezo quarrington", Source: "x"}}},
		},
		Decision: validDecision(),
	}
	if !hasError(dictionary.Validate(d)) {
		t.Error("want an error issue for a case-insensitive duplicate canonical name")
	}
}

func TestValidate_AliasCollidingWithAnotherEntrysCanonicalNameIsError(t *testing.T) {
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{
			{Name: "a", Entries: []dictionary.Entry{{CanonicalName: "Ezo Quarrington", Aliases: []string{"Ezo"}, Source: "x"}}},
			{Name: "b", Entries: []dictionary.Entry{{CanonicalName: "Ezo", Source: "x"}}},
		},
		Decision: validDecision(),
	}
	if !hasError(dictionary.Validate(d)) {
		t.Error("want an error issue when an alias collides with another entry's canonical name")
	}
}

func TestValidate_DanglingRequiresAnyOfIsError(t *testing.T) {
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{
			{Name: "a", Weight: 1, RequiresAnyOf: []string{"does_not_exist"}, Entries: []dictionary.Entry{{CanonicalName: "x", Source: "y"}}},
		},
		Decision: validDecision(),
	}
	if !hasError(dictionary.Validate(d)) {
		t.Error("want an error issue for a requires_any_of reference to an unknown category")
	}
}

func TestValidate_EmptyCanonicalNameIsError(t *testing.T) {
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{{Name: "a", Entries: []dictionary.Entry{{Source: "x"}}}},
		Decision:   validDecision(),
	}
	if !hasError(dictionary.Validate(d)) {
		t.Error("want an error issue for an entry with no canonical_name")
	}
}

func TestValidate_ActiveUntilBeforeActiveSinceIsError(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{{Name: "a", Entries: []dictionary.Entry{
			{CanonicalName: "x", Source: "y", ActiveSince: &since, ActiveUntil: &until},
		}}},
		Decision: validDecision(),
	}
	if !hasError(dictionary.Validate(d)) {
		t.Error("want an error issue when active_until is before active_since")
	}
}

func TestValidate_ConfidenceOutOfRangeIsError(t *testing.T) {
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{{Name: "a", Entries: []dictionary.Entry{{CanonicalName: "x", Source: "y", Confidence: 1.5}}}},
		Decision:   validDecision(),
	}
	if !hasError(dictionary.Validate(d)) {
		t.Error("want an error issue for confidence outside [0,1]")
	}
}

func TestValidate_EmptySourceIsWarningNotError(t *testing.T) {
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{{Name: "a", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "x"}}}},
		Decision:   validDecision(),
	}
	issues := dictionary.Validate(d)
	if hasError(issues) {
		t.Errorf("empty source should only warn, got errors in %v", issues)
	}
	if !hasWarning(issues) {
		t.Error("want a warning issue for an entry with no source")
	}
}

func TestValidate_ZeroWeightCategoryIsWarning(t *testing.T) {
	d := dictionary.Dictionary{
		Categories: []dictionary.Category{{Name: "dead", Weight: 0, Entries: []dictionary.Entry{{CanonicalName: "x", Source: "y"}}}},
		Decision:   validDecision(),
	}
	if !hasWarning(dictionary.Validate(d)) {
		t.Error("want a warning issue for a category with weight 0")
	}
}

func TestValidate_ThresholdSanity(t *testing.T) {
	tests := []struct {
		name string
		mut  func(d *dictionary.Decision)
	}{
		{"max_count_per_term < 1", func(d *dictionary.Decision) { d.MaxCountPerTerm = 0 }},
		{"title_weight_multiplier < 1", func(d *dictionary.Decision) { d.TitleWeightMultiplier = 0 }},
		{"negative_threshold_high > 0", func(d *dictionary.Decision) { d.NegativeThresholdHigh = 1 }},
		{"positive_threshold_medium < 0", func(d *dictionary.Decision) { d.PositiveThresholdMedium = -1 }},
		{"positive_threshold_high < positive_threshold_medium", func(d *dictionary.Decision) { d.PositiveThresholdHigh = 1; d.PositiveThresholdMedium = 5 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := validDecision()
			tt.mut(&decision)
			d := dictionary.Dictionary{Decision: decision}
			if !hasError(dictionary.Validate(d)) {
				t.Errorf("want an error issue for %s", tt.name)
			}
		})
	}
}

func TestValidate_SchemaV2_RejectsUnknownRosterStatus(t *testing.T) {
	d := dictionary.Dictionary{
		SchemaVersion: 2,
		Categories: []dictionary.Category{
			{Name: "active_roster_core", Weight: 1, Entries: []dictionary.Entry{
				{CanonicalName: "Some Player", RosterStatus: "benched_forever"},
			}},
		},
		Decision: validDecision(),
	}

	issues := dictionary.Validate(d)
	hasError := false
	for _, i := range issues {
		if i.Severity == dictionary.SeverityError {
			hasError = true
		}
	}
	if !hasError {
		t.Fatalf("expected an error for an unknown roster_status under schema_version 2, got %+v", issues)
	}
}

func TestValidate_SchemaV1_DoesNotCheckRosterStatus(t *testing.T) {
	d := dictionary.Dictionary{
		SchemaVersion: 0, // absent / v1
		Categories: []dictionary.Category{
			{Name: "active_roster_core", Weight: 1, Entries: []dictionary.Entry{
				{CanonicalName: "Some Player", RosterStatus: "anything-goes-pre-v2"},
			}},
		},
		Decision: validDecision(),
	}

	issues := dictionary.Validate(d)
	for _, i := range issues {
		if i.Severity == dictionary.SeverityError {
			t.Fatalf("v1 dictionaries must not be checked against the v2 roster_status enum, got %+v", issues)
		}
	}
}

func TestValidate_SchemaV2_RejectsChecksumMismatch(t *testing.T) {
	d := dictionary.Dictionary{
		SchemaVersion: 2,
		Checksum:      "not-the-real-hash",
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes"}}},
		},
		Decision: validDecision(),
	}

	issues := dictionary.Validate(d)
	hasError := false
	for _, i := range issues {
		if i.Severity == dictionary.SeverityError {
			hasError = true
		}
	}
	if !hasError {
		t.Fatalf("expected an error for a checksum that doesn't match ContentHash, got %+v", issues)
	}
}

func TestValidate_SchemaV2_AcceptsCorrectChecksum(t *testing.T) {
	d := dictionary.Dictionary{
		SchemaVersion: 2,
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes"}}},
		},
		Decision: validDecision(),
	}
	d.Checksum = dictionary.ContentHash(d)

	issues := dictionary.Validate(d)
	for _, i := range issues {
		if i.Severity == dictionary.SeverityError {
			t.Fatalf("expected no error with a correct checksum, got %+v", issues)
		}
	}
}

func hasError(issues []dictionary.Issue) bool {
	for _, i := range issues {
		if i.Severity == dictionary.SeverityError {
			return true
		}
	}
	return false
}

func hasWarning(issues []dictionary.Issue) bool {
	for _, i := range issues {
		if i.Severity == dictionary.SeverityWarning {
			return true
		}
	}
	return false
}
