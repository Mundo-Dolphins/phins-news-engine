package dictionary

import (
	"fmt"
	"strings"
)

// IssueSeverity classifies a Validate finding.
type IssueSeverity string

const (
	SeverityError   IssueSeverity = "error"
	SeverityWarning IssueSeverity = "warning"
)

// Issue is one Validate finding.
type Issue struct {
	Severity IssueSeverity
	Category string
	Entry    string
	Message  string
}

// String renders Issue as a single line for CLI output and log lines.
func (i Issue) String() string {
	loc := i.Category
	if i.Entry != "" {
		loc += "/" + i.Entry
	}
	if loc == "" {
		return fmt.Sprintf("[%s] %s", i.Severity, i.Message)
	}
	return fmt.Sprintf("[%s] %s: %s", i.Severity, loc, i.Message)
}

// Validate checks d for structural problems: duplicate canonical
// names/aliases (case-insensitive, across the whole dictionary), dangling
// requires_any_of references, inconsistent active dates, out-of-range
// confidence, and threshold sanity. It never reads files or mutates d —
// both the startup loader and the `dictionary validate` CLI command call
// it on an already-decoded Dictionary.
func Validate(d Dictionary) []Issue {
	var issues []Issue

	categoryNames := make(map[string]bool, len(d.Categories))
	for _, cat := range d.Categories {
		categoryNames[cat.Name] = true
	}

	names := map[string]string{} // lowercased name/alias -> "category/entry" that registered it first
	for _, cat := range d.Categories {
		if cat.Weight == 0 {
			issues = append(issues, Issue{Severity: SeverityWarning, Category: cat.Name, Message: "weight is 0, this category never contributes to scoring"})
		}
		for _, required := range cat.RequiresAnyOf {
			if !categoryNames[required] {
				issues = append(issues, Issue{Severity: SeverityError, Category: cat.Name, Message: fmt.Sprintf("requires_any_of references unknown category %q", required)})
			}
		}
		for _, entry := range cat.Entries {
			issues = append(issues, validateEntry(cat.Name, entry, names)...)
		}
	}

	issues = append(issues, validateDecision(d.Decision)...)

	if d.SchemaVersion >= 2 {
		issues = append(issues, validateSchemaV2(d)...)
	}
	return issues
}

func validateSchemaV2(d Dictionary) []Issue {
	var issues []Issue

	knownRosterStatus := map[RosterStatus]bool{
		"":                         true,
		RosterStatusActive:         true,
		RosterStatusPracticeSquad:  true,
		RosterStatusInjuredReserve: true,
		RosterStatusReserveOther:   true,
	}
	for _, cat := range d.Categories {
		for _, e := range cat.Entries {
			if !knownRosterStatus[e.RosterStatus] {
				issues = append(issues, Issue{
					Severity: SeverityError, Category: cat.Name, Entry: e.CanonicalName,
					Message: fmt.Sprintf("unknown roster_status %q", e.RosterStatus),
				})
			}
		}
	}

	if d.Checksum != "" {
		want := ContentHash(d)
		if d.Checksum != want {
			issues = append(issues, Issue{
				Severity: SeverityError, Category: "", Entry: "",
				Message: fmt.Sprintf("meta.checksum %q does not match computed content hash %q", d.Checksum, want),
			})
		}
	}

	return issues
}

func validateEntry(categoryName string, e Entry, names map[string]string) []Issue {
	var issues []Issue
	if e.CanonicalName == "" {
		return append(issues, Issue{Severity: SeverityError, Category: categoryName, Message: "canonical_name is required"})
	}
	issues = append(issues, checkDuplicate(categoryName, e.CanonicalName, e.CanonicalName, names)...)
	for _, alias := range e.Aliases {
		issues = append(issues, checkDuplicate(categoryName, e.CanonicalName, alias, names)...)
	}
	if e.ActiveSince != nil && e.ActiveUntil != nil && e.ActiveUntil.Before(*e.ActiveSince) {
		issues = append(issues, Issue{Severity: SeverityError, Category: categoryName, Entry: e.CanonicalName, Message: "active_until is before active_since"})
	}
	if e.Confidence < 0 || e.Confidence > 1 {
		issues = append(issues, Issue{Severity: SeverityError, Category: categoryName, Entry: e.CanonicalName, Message: fmt.Sprintf("confidence %.2f is outside [0,1]", e.Confidence)})
	}
	if e.Source == "" {
		issues = append(issues, Issue{Severity: SeverityWarning, Category: categoryName, Entry: e.CanonicalName, Message: "source is empty"})
	}
	return issues
}

func checkDuplicate(categoryName, canonicalName, term string, names map[string]string) []Issue {
	key := strings.ToLower(term)
	if key == "" {
		return nil
	}
	if existing, ok := names[key]; ok {
		return []Issue{{Severity: SeverityError, Category: categoryName, Entry: canonicalName, Message: fmt.Sprintf("term %q duplicates an entry already registered by %s", term, existing)}}
	}
	names[key] = categoryName + "/" + canonicalName
	return nil
}

func validateDecision(d Decision) []Issue {
	var issues []Issue
	if d.MaxCountPerTerm < 1 {
		issues = append(issues, Issue{Severity: SeverityError, Message: "decision.max_count_per_term must be at least 1"})
	}
	if d.TitleWeightMultiplier < 1 {
		issues = append(issues, Issue{Severity: SeverityError, Message: "decision.title_weight_multiplier must be at least 1"})
	}
	if d.NegativeThresholdHigh > 0 {
		issues = append(issues, Issue{Severity: SeverityError, Message: "decision.negative_threshold_high must be <= 0"})
	}
	if d.PositiveThresholdMedium < 0 {
		issues = append(issues, Issue{Severity: SeverityError, Message: "decision.positive_threshold_medium must be >= 0"})
	}
	if d.PositiveThresholdHigh < d.PositiveThresholdMedium {
		issues = append(issues, Issue{Severity: SeverityError, Message: "decision.positive_threshold_high must be >= positive_threshold_medium"})
	}
	return issues
}
