package dictionaryupdate_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func rosterDictionary(entries ...dictionary.Entry) dictionary.Dictionary {
	return dictionary.Dictionary{
		Categories: []dictionary.Category{
			{Name: "active_roster_core", Weight: 1, Entries: entries},
		},
	}
}

var defaultPolicy = dictionaryupdate.DeactivationPolicy{MinimumSuccessfulAbsences: 2, MinimumElapsedDays: 3}

func TestCompare_NewEntityProducesAddEntityProposal(t *testing.T) {
	observed := []dictionaryupdate.SourceEntity{
		{DisplayName: "Ezo Quarrington", CanonicalKey: "ezo quarrington", Kind: dictionaryupdate.SourceKindRoster, Position: "QB", Number: "1"},
	}

	proposals := dictionaryupdate.Compare(rosterDictionary(), observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	if len(proposals) != 1 || proposals[0].Kind != dictionaryupdate.ProposalAddEntity {
		t.Fatalf("got %+v", proposals)
	}
}

func TestCompare_UnchangedEntityProducesNoChange(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Ezo Quarrington", Position: "QB", Number: "1"})
	observed := []dictionaryupdate.SourceEntity{
		{DisplayName: "Ezo Quarrington", CanonicalKey: "ezo quarrington", Kind: dictionaryupdate.SourceKindRoster, Position: "QB", Number: "1"},
	}

	proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	if len(proposals) != 1 || proposals[0].Kind != dictionaryupdate.ProposalNoChange {
		t.Fatalf("got %+v", proposals)
	}
}

func TestCompare_PositionAndNumberChangeDetected(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Some Player", Position: "RB", Number: "22"})
	observed := []dictionaryupdate.SourceEntity{
		{DisplayName: "Some Player", CanonicalKey: "some player", Kind: dictionaryupdate.SourceKindRoster, Position: "WR", Number: "10"},
	}

	proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	kinds := map[dictionaryupdate.ProposalKind]bool{}
	for _, p := range proposals {
		kinds[p.Kind] = true
	}
	if !kinds[dictionaryupdate.ProposalUpdatePosition] || !kinds[dictionaryupdate.ProposalUpdateNumber] {
		t.Fatalf("expected UpdatePosition and UpdateNumber, got %+v", proposals)
	}
}

// TestCompare_SimultaneousFieldChangesAreDeterministicallyOrdered proves the
// final sort has a total order over proposals: when a single entity produces
// multiple proposals that tie on (Category, CanonicalKey) — as
// TestCompare_PositionAndNumberChangeDetected's scenario does — the result
// must be reproducible run-to-run, not merely "contains both kinds". Kind is
// used as the tertiary sort key, so for this entity the exact expected order
// is UpdateNumber ("update_number") before UpdatePosition ("update_position"),
// since "update_number" < "update_position" lexicographically.
func TestCompare_SimultaneousFieldChangesAreDeterministicallyOrdered(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Some Player", Position: "RB", Number: "22"})
	observed := []dictionaryupdate.SourceEntity{
		{DisplayName: "Some Player", CanonicalKey: "some player", Kind: dictionaryupdate.SourceKindRoster, Position: "WR", Number: "10"},
	}

	for i := 0; i < 25; i++ {
		proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

		if len(proposals) != 2 {
			t.Fatalf("run %d: expected exactly 2 proposals, got %+v", i, proposals)
		}
		if proposals[0].Kind != dictionaryupdate.ProposalUpdateNumber || proposals[1].Kind != dictionaryupdate.ProposalUpdatePosition {
			t.Fatalf("run %d: expected deterministic order [UpdateNumber, UpdatePosition], got [%s, %s]",
				i, proposals[0].Kind, proposals[1].Kind)
		}
	}
}

func TestCompare_ReactivationDetected(t *testing.T) {
	past := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Old Player", ActiveUntil: &past})
	observed := []dictionaryupdate.SourceEntity{
		{DisplayName: "Old Player", CanonicalKey: "old player", Kind: dictionaryupdate.SourceKindRoster},
	}

	proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	found := false
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalReactivateEntity {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ReactivateEntity, got %+v", proposals)
	}
}

func TestCompare_SingleAbsenceIsNeedsReviewNotDeactivate(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Missing Player"})

	proposals := dictionaryupdate.Compare(current, nil, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	if len(proposals) != 1 || proposals[0].Kind != dictionaryupdate.ProposalNeedsReview {
		t.Fatalf("a single absence must never directly propose deactivation, got %+v", proposals)
	}
}

func TestCompare_RepeatedAbsenceOverPolicyThresholdProposesDeactivation(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Missing Player"})
	firstAbsent := time.Now().Add(-5 * 24 * time.Hour)
	absences := dictionaryupdate.AbsenceLog{Records: map[string]dictionaryupdate.AbsenceRecord{
		"missing player|roster": {CanonicalKey: "missing player", Source: dictionaryupdate.SourceKindRoster, FirstAbsentAt: firstAbsent, ConsecutiveAbsences: 1},
	}}

	proposals := dictionaryupdate.Compare(current, nil, absences, defaultPolicy, time.Now())

	if len(proposals) != 1 || proposals[0].Kind != dictionaryupdate.ProposalDeactivateEntity {
		t.Fatalf("expected DeactivateEntity after policy threshold met, got %+v", proposals)
	}
}

func TestCompare_DuplicateObservedCanonicalKeyIsConflict(t *testing.T) {
	observed := []dictionaryupdate.SourceEntity{
		{DisplayName: "John Smith", CanonicalKey: "john smith", Kind: dictionaryupdate.SourceKindRoster, Position: "WR"},
		{DisplayName: "John Smith", CanonicalKey: "john smith", Kind: dictionaryupdate.SourceKindCoaches, Role: "Assistant"},
	}

	proposals := dictionaryupdate.Compare(rosterDictionary(), observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	if len(proposals) != 1 || proposals[0].Kind != dictionaryupdate.ProposalConflict {
		t.Fatalf("expected a single Conflict proposal, got %+v", proposals)
	}
}

func TestCompare_NewAliasCandidateProducesAddAlias(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Ezo Quarrington", Position: "QB", Number: "1"})
	observed := []dictionaryupdate.SourceEntity{
		{
			DisplayName: "Ezo Quarrington", CanonicalKey: "ezo quarrington", Kind: dictionaryupdate.SourceKindRoster,
			Position: "QB", Number: "1",
			Aliases: []dictionaryupdate.AliasCandidate{{Value: "T. Quarrington", Class: dictionaryupdate.AliasClassInitialLast}},
		},
	}

	proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	if len(proposals) != 1 || proposals[0].Kind != dictionaryupdate.ProposalAddAlias {
		t.Fatalf("got %+v", proposals)
	}
	if proposals[0].AliasProposal == nil || proposals[0].AliasProposal.Value != "T. Quarrington" {
		t.Fatalf("AliasProposal not populated with the specific candidate: %+v", proposals[0].AliasProposal)
	}
}

func TestCompare_AmbiguousAliasProducesMarkAliasAmbiguous(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Tavion Dale"})
	observed := []dictionaryupdate.SourceEntity{
		{
			DisplayName: "Tavion Dale", CanonicalKey: "tavion dale", Kind: dictionaryupdate.SourceKindRoster,
			Aliases: []dictionaryupdate.AliasCandidate{{Value: "Dale", Class: dictionaryupdate.AliasClassAmbiguous}},
		},
	}

	proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	if len(proposals) != 1 || proposals[0].Kind != dictionaryupdate.ProposalMarkAliasAmbiguous {
		t.Fatalf("got %+v", proposals)
	}
}

func TestCompare_AliasOwnedByDifferentEntityIsConflict(t *testing.T) {
	current := rosterDictionary(
		dictionary.Entry{CanonicalName: "Jaylen Wickersham", Aliases: []string{"J. Wickersham"}},
		dictionary.Entry{CanonicalName: "Some Other Player"},
	)
	observed := []dictionaryupdate.SourceEntity{
		{
			DisplayName: "Some Other Player", CanonicalKey: "some other player", Kind: dictionaryupdate.SourceKindRoster,
			Aliases: []dictionaryupdate.AliasCandidate{{Value: "J. Wickersham", Class: dictionaryupdate.AliasClassInitialLast}},
		},
	}

	proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, defaultPolicy, time.Now())

	found := false
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalConflict && len(p.ConflictWith) == 1 && p.ConflictWith[0] == "jaylen wickersham" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an alias-ownership Conflict referencing jaylen wickersham, got %+v", proposals)
	}
}
