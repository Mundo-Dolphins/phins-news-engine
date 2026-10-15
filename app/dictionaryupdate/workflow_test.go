package dictionaryupdate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

type scriptedPort struct {
	actions []dictionaryupdate.Action
	edited  map[int]*dictionaryupdate.Proposal
	calls   int
}

func (p *scriptedPort) Next(_ context.Context, _ dictionaryupdate.Proposal, _ dictionaryupdate.ReviewState) (dictionaryupdate.Action, *dictionaryupdate.Proposal, error) {
	a := p.actions[p.calls]
	e := p.edited[p.calls]
	p.calls++
	return a, e, nil
}

func TestReviewProposals_AcceptRejectSkip(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a"},
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "b"},
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "c"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{
		dictionaryupdate.ActionAccept, dictionaryupdate.ActionReject, dictionaryupdate.ActionSkip,
	}}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, nil)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	want := []dictionaryupdate.ReviewOutcome{dictionaryupdate.ReviewOutcomeAccepted, dictionaryupdate.ReviewOutcomeRejected, dictionaryupdate.ReviewOutcomeSkipped}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d", len(results), len(want))
	}
	for i, w := range want {
		if results[i].Outcome != w {
			t.Errorf("results[%d].Outcome = %q, want %q", i, results[i].Outcome, w)
		}
	}
}

func TestReviewProposals_AcceptAllOfTypeAppliesToLaterSameKindProposals(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddAlias, CanonicalKey: "a"},
		{Kind: dictionaryupdate.ProposalAddAlias, CanonicalKey: "b"},
		{Kind: dictionaryupdate.ProposalAddAlias, CanonicalKey: "c"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{dictionaryupdate.ActionAcceptAllOfType}}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, nil)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for _, r := range results {
		if r.Outcome != dictionaryupdate.ReviewOutcomeAccepted {
			t.Errorf("expected all AddAlias proposals accepted, got %+v", r)
		}
	}
	if port.calls != 1 {
		t.Errorf("expected the port consulted once (for the accept-all decision), got %d calls", port.calls)
	}
}

func TestReviewProposals_EditReturnsModifiedProposal(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a", Detail: "original"}}
	edited := &dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a", Detail: "edited"}
	port := &scriptedPort{
		actions: []dictionaryupdate.Action{dictionaryupdate.ActionEdit},
		edited:  map[int]*dictionaryupdate.Proposal{0: edited},
	}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, nil)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if len(results) != 1 || results[0].Proposal.Detail != "edited" {
		t.Fatalf("got %+v", results)
	}
}

func TestReviewProposals_AbortReturnsErrReviewAborted(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a"}}
	port := &scriptedPort{actions: []dictionaryupdate.Action{dictionaryupdate.ActionAbort}}

	_, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, nil)
	if !errors.Is(err, dictionaryupdate.ErrReviewAborted) {
		t.Fatalf("expected ErrReviewAborted, got %v", err)
	}
}

func TestReviewProposals_QuitAndSaveStopsWithoutError(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a"},
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "b"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{dictionaryupdate.ActionAccept, dictionaryupdate.ActionQuitAndSave}}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, nil)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result before quit-and-save, got %d", len(results))
	}
}

func TestReviewProposals_BackRevisitsPreviousProposalWithoutDuplicating(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a"},
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "b"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{
		dictionaryupdate.ActionAccept, dictionaryupdate.ActionBack, dictionaryupdate.ActionAccept, dictionaryupdate.ActionAccept,
	}}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, nil)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (back must not leave duplicate entries)", len(results))
	}
}

func TestReviewProposals_BackAtResumeBoundaryDoesNotRegressBelowStartAt(t *testing.T) {
	// Simulates a resumed session: startAt > 0 because proposals before
	// it were already decided and persisted in a prior invocation. This
	// invocation's own results slice starts empty and must never regain
	// an entry for an index it never decided.
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a"},
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "b"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{
		dictionaryupdate.ActionBack, dictionaryupdate.ActionAccept,
	}}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 1, port, nil)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if port.calls != 2 {
		t.Fatalf("expected back-at-boundary to be a no-op re-prompt (2 port calls total), got %d", port.calls)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (index 0 was not part of this invocation)", len(results))
	}
	if results[0].Proposal.CanonicalKey != "b" {
		t.Errorf("expected the decided proposal to be %q (this invocation's startAt), got %q", "b", results[0].Proposal.CanonicalKey)
	}
}

func TestReviewProposals_BackAtIndexZeroWithStartAtZeroIsSafeNoOp(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{
		dictionaryupdate.ActionBack, dictionaryupdate.ActionAccept,
	}}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, nil)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != dictionaryupdate.ReviewOutcomeAccepted {
		t.Fatalf("got %+v", results)
	}
	if port.calls != 2 {
		t.Fatalf("expected 2 port calls (no-op back + accept), got %d", port.calls)
	}
}

func TestReviewProposals_SaveProgressFiresForEachBlanketExpandedDecision(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddAlias, CanonicalKey: "a"},
		{Kind: dictionaryupdate.ProposalAddAlias, CanonicalKey: "b"},
		{Kind: dictionaryupdate.ProposalAddAlias, CanonicalKey: "c"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{dictionaryupdate.ActionAcceptAllOfType}}

	var savedCursors []int
	var savedKeys []string
	save := func(cursor int, r dictionaryupdate.ReviewResult) error {
		savedCursors = append(savedCursors, cursor)
		savedKeys = append(savedKeys, r.Proposal.CanonicalKey)
		return nil
	}

	results, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, save)
	if err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	wantCursors := []int{1, 2, 3}
	if len(savedCursors) != len(wantCursors) {
		t.Fatalf("expected save called once per blanket-expanded decision (3 times), got %d calls: %v", len(savedCursors), savedCursors)
	}
	for i, w := range wantCursors {
		if savedCursors[i] != w {
			t.Errorf("savedCursors[%d] = %d, want %d", i, savedCursors[i], w)
		}
	}
	wantKeys := []string{"a", "b", "c"}
	for i, w := range wantKeys {
		if savedKeys[i] != w {
			t.Errorf("savedKeys[%d] = %q, want %q", i, savedKeys[i], w)
		}
	}
}

func TestReviewProposals_SavesProgressAfterEveryDecision(t *testing.T) {
	proposals := []dictionaryupdate.Proposal{
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a"},
		{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "b"},
	}
	port := &scriptedPort{actions: []dictionaryupdate.Action{dictionaryupdate.ActionAccept, dictionaryupdate.ActionReject}}

	var savedCursors []int
	save := func(cursor int, _ dictionaryupdate.ReviewResult) error {
		savedCursors = append(savedCursors, cursor)
		return nil
	}

	if _, err := dictionaryupdate.ReviewProposals(context.Background(), proposals, 0, port, save); err != nil {
		t.Fatalf("ReviewProposals: %v", err)
	}
	if len(savedCursors) != 2 || savedCursors[0] != 1 || savedCursors[1] != 2 {
		t.Fatalf("expected save called with cursors [1 2], got %v", savedCursors)
	}
}

func TestApplyDecisions_AddsAcceptedNewEntityAndSkipsRejected(t *testing.T) {
	current := rosterDictionary()
	added := dictionaryupdate.SourceEntity{DisplayName: "Ezo Quarrington", CanonicalKey: "ezo quarrington", Kind: dictionaryupdate.SourceKindRoster, Position: "QB", Number: "1", Source: "isotopes.example.com"}
	rejected := dictionaryupdate.SourceEntity{DisplayName: "Rejected Player", CanonicalKey: "rejected player", Kind: dictionaryupdate.SourceKindRoster}

	results := []dictionaryupdate.ReviewResult{
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "ezo quarrington", Category: "active_roster_core", Observed: &added}},
		{Outcome: dictionaryupdate.ReviewOutcomeRejected, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "rejected player", Category: "active_roster_core", Observed: &rejected}},
	}

	candidate, changeCount := dictionaryupdate.ApplyDecisions(current, results)

	if changeCount != 1 {
		t.Fatalf("changeCount = %d, want 1", changeCount)
	}
	if len(candidate.Categories) != 1 || len(candidate.Categories[0].Entries) != 1 {
		t.Fatalf("expected exactly the accepted entity to be added, got %+v", candidate.Categories)
	}
	got := candidate.Categories[0].Entries[0]
	if got.CanonicalName != "Ezo Quarrington" || got.Position != "QB" || got.Number != "1" {
		t.Fatalf("got %+v", got)
	}
}

func TestApplyDecisions_UpdatesPositionNumberRosterStatus(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Some Player", Position: "RB", Number: "22"})
	obs := dictionaryupdate.SourceEntity{DisplayName: "Some Player", CanonicalKey: "some player", Position: "WR", Number: "10", RosterStatus: dictionary.RosterStatusPracticeSquad}

	results := []dictionaryupdate.ReviewResult{
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalUpdatePosition, CanonicalKey: "some player", Category: "active_roster_core", Observed: &obs}},
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalUpdateNumber, CanonicalKey: "some player", Category: "active_roster_core", Observed: &obs}},
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalUpdateRosterStatus, CanonicalKey: "some player", Category: "active_roster_core", Observed: &obs}},
	}

	candidate, changeCount := dictionaryupdate.ApplyDecisions(current, results)

	if changeCount != 3 {
		t.Fatalf("changeCount = %d, want 3", changeCount)
	}
	got := candidate.Categories[0].Entries[0]
	if got.Position != "WR" || got.Number != "10" || got.RosterStatus != dictionary.RosterStatusPracticeSquad {
		t.Fatalf("got %+v", got)
	}
}

func TestApplyDecisions_AddsOnlyTheSpecificAcceptedAlias(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Jaylen Wickersham"})
	obs := dictionaryupdate.SourceEntity{
		DisplayName: "Jaylen Wickersham", CanonicalKey: "jaylen wickersham",
		Aliases: []dictionaryupdate.AliasCandidate{
			{Value: "J. Wickersham", Class: dictionaryupdate.AliasClassInitialLast},
			{Value: "Wickersham", Class: dictionaryupdate.AliasClassAmbiguous},
		},
	}
	accepted := dictionaryupdate.AliasCandidate{Value: "J. Wickersham", Class: dictionaryupdate.AliasClassInitialLast}

	results := []dictionaryupdate.ReviewResult{
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{
			Kind: dictionaryupdate.ProposalAddAlias, CanonicalKey: "jaylen wickersham", Category: "active_roster_core",
			Observed: &obs, AliasProposal: &accepted,
		}},
	}

	candidate, changeCount := dictionaryupdate.ApplyDecisions(current, results)

	if changeCount != 1 {
		t.Fatalf("changeCount = %d, want 1", changeCount)
	}
	got := candidate.Categories[0].Entries[0].Aliases
	if len(got) != 1 || got[0] != "J. Wickersham" {
		t.Fatalf("expected only the specifically-accepted alias to be added, got %v", got)
	}
}

func TestApplyDecisions_ConflictAndNeedsReviewAreNeverApplied(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Some Player"})
	results := []dictionaryupdate.ReviewResult{
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalConflict, CanonicalKey: "some player"}},
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalNeedsReview, CanonicalKey: "some player"}},
	}

	_, changeCount := dictionaryupdate.ApplyDecisions(current, results)

	if changeCount != 0 {
		t.Fatalf("Conflict/NeedsReview must never be applied even if marked accepted, changeCount = %d", changeCount)
	}
}

func TestWriteCandidate_BumpsVersionAndSetsMetadata(t *testing.T) {
	// Decision must satisfy dictionary.Validate's threshold sanity checks
	// (MaxCountPerTerm >= 1, TitleWeightMultiplier >= 1, ...), which apply
	// unconditionally regardless of SchemaVersion -- otherwise this
	// fixture itself would trip validation before WriteCandidate's own
	// version-bump/metadata behavior is ever exercised.
	validDecision := dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3}
	current := dictionary.Dictionary{Version: "1.2.3", Categories: []dictionary.Category{{Name: "official_names", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes"}}}}, Decision: validDecision}
	candidate := current // no structural changes for this test, just metadata assignment

	var backedUp, written bool
	var order []string
	written1, err := dictionaryupdate.WriteCandidate(current, candidate, 2, []string{"roster"}, "fetched-x.json", time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		func(dictionary.Dictionary) error { backedUp = true; order = append(order, "backup"); return nil },
		func(d dictionary.Dictionary) error {
			written = true
			order = append(order, "write")
			candidate = d
			return nil
		},
	)
	if err != nil {
		t.Fatalf("WriteCandidate: %v", err)
	}
	if !backedUp || !written {
		t.Fatalf("expected both backup and write to be called: backedUp=%v written=%v", backedUp, written)
	}
	if len(order) != 2 || order[0] != "backup" || order[1] != "write" {
		t.Fatalf("expected backup before write, got order %v", order)
	}
	if written1.Version != "1.2.4" {
		t.Errorf("Version = %q, want 1.2.4", written1.Version)
	}
	if written1.SchemaVersion != 2 || written1.ChangeCount != 2 || written1.SnapshotRef != "fetched-x.json" || written1.Tool == "" {
		t.Errorf("got %+v", written1)
	}
	if written1.Checksum != dictionary.ContentHash(written1) {
		t.Errorf("Checksum does not match ContentHash of the written candidate")
	}
}

func TestWriteCandidate_SecondRunWithStampedSchemaV2ChecksumSucceeds(t *testing.T) {
	// Regression test: once a dictionary has already been through
	// WriteCandidate once (SchemaVersion == 2, Checksum stamped to match
	// its own content), a second real run must not have its
	// content-changing candidate rejected by comparing the STALE
	// checksum (copied byte-for-byte from current by ApplyDecisions,
	// which only mutates Categories) against the content hash of the
	// NEW candidate.
	validDecision := dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3}
	current := dictionary.Dictionary{
		Version: "1.2.3", SchemaVersion: 2,
		Categories: []dictionary.Category{{Name: "active_roster_core", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "Some Player", Source: "isotopes.example.com"}}}},
		Decision:   validDecision,
	}
	current.Checksum = dictionary.ContentHash(current)

	added := dictionaryupdate.SourceEntity{DisplayName: "Ezo Quarrington", CanonicalKey: "ezo quarrington", Kind: dictionaryupdate.SourceKindRoster, Position: "QB", Number: "1", Source: "isotopes.example.com"}
	results := []dictionaryupdate.ReviewResult{
		{Outcome: dictionaryupdate.ReviewOutcomeAccepted, Proposal: dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "ezo quarrington", Category: "active_roster_core", Observed: &added}},
	}
	candidate, changeCount := dictionaryupdate.ApplyDecisions(current, results)
	if changeCount != 1 {
		t.Fatalf("changeCount = %d, want 1", changeCount)
	}
	// Sanity: ApplyDecisions really did carry over the stale checksum
	// unchanged, and it really is stale relative to the new content --
	// otherwise this test wouldn't be exercising the bug at all.
	if candidate.Checksum != current.Checksum {
		t.Fatalf("expected ApplyDecisions to carry over current's Checksum unchanged, got %q want %q", candidate.Checksum, current.Checksum)
	}
	if candidate.Checksum == dictionary.ContentHash(candidate) {
		t.Fatalf("test setup invalid: candidate's stale checksum unexpectedly matches its new content hash")
	}

	var backedUp, written bool
	_, err := dictionaryupdate.WriteCandidate(current, candidate, changeCount, []string{"roster"}, "fetched-x.json", time.Now(),
		func(dictionary.Dictionary) error { backedUp = true; return nil },
		func(dictionary.Dictionary) error { written = true; return nil },
	)
	if err != nil {
		t.Fatalf("WriteCandidate: %v (second-run steady-state update must not fail checksum validation)", err)
	}
	if !backedUp || !written {
		t.Fatalf("expected both backup and write to be called: backedUp=%v written=%v", backedUp, written)
	}
}

func TestWriteCandidate_FailsValidationWithoutCallingBackupOrWrite(t *testing.T) {
	current := dictionary.Dictionary{Version: "1.0.0"}
	invalidCandidate := dictionary.Dictionary{
		Version: "1.0.0",
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes", Confidence: 5}}}, // out of [0,1] range -> error
		},
	}

	called := false
	_, err := dictionaryupdate.WriteCandidate(current, invalidCandidate, 1, nil, "", time.Now(),
		func(dictionary.Dictionary) error { called = true; return nil },
		func(dictionary.Dictionary) error { called = true; return nil },
	)
	if err == nil {
		t.Fatal("expected an error for an invalid candidate")
	}
	if called {
		t.Fatal("backup/write must never be called when validation fails")
	}
}

func TestUpdateAbsenceLog_IncrementsForStillAbsentEntity(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Missing Player"})
	firstAbsent := time.Now().Add(-3 * 24 * time.Hour)
	prior := dictionaryupdate.AbsenceLog{Records: map[string]dictionaryupdate.AbsenceRecord{
		"missing player|roster": {CanonicalKey: "missing player", Source: dictionaryupdate.SourceKindRoster, FirstAbsentAt: firstAbsent, ConsecutiveAbsences: 1},
	}}

	updated := dictionaryupdate.UpdateAbsenceLog(current, nil, prior, time.Now())

	rec, ok := updated.Get("missing player", dictionaryupdate.SourceKindRoster)
	if !ok {
		t.Fatal("expected an absence record for the still-missing entity")
	}
	if rec.ConsecutiveAbsences != 2 {
		t.Errorf("ConsecutiveAbsences = %d, want 2", rec.ConsecutiveAbsences)
	}
	if !rec.FirstAbsentAt.Equal(firstAbsent) {
		t.Errorf("FirstAbsentAt should be preserved across runs, got %v want %v", rec.FirstAbsentAt, firstAbsent)
	}
}

func TestUpdateAbsenceLog_ClearsRecordWhenEntityReappears(t *testing.T) {
	current := rosterDictionary(dictionary.Entry{CanonicalName: "Back Again"})
	prior := dictionaryupdate.AbsenceLog{Records: map[string]dictionaryupdate.AbsenceRecord{
		"back again|roster": {CanonicalKey: "back again", Source: dictionaryupdate.SourceKindRoster, ConsecutiveAbsences: 1},
	}}
	observed := []dictionaryupdate.SourceEntity{{CanonicalKey: "back again", Kind: dictionaryupdate.SourceKindRoster}}

	updated := dictionaryupdate.UpdateAbsenceLog(current, observed, prior, time.Now())

	if _, ok := updated.Get("back again", dictionaryupdate.SourceKindRoster); ok {
		t.Fatal("expected the absence record to be cleared once the entity reappears")
	}
}

func TestIsSafeAddition(t *testing.T) {
	fullEntity := &dictionaryupdate.SourceEntity{DisplayName: "Ezo Quarrington", CanonicalKey: "ezo quarrington", ProfileURL: "/player/ezo", Source: "isotopes.example.com"}
	initialsOnly := &dictionaryupdate.SourceEntity{DisplayName: "T. Quarrington", CanonicalKey: "t quarrington", ProfileURL: "/player/ezo", Source: "isotopes.example.com"}
	noProfile := &dictionaryupdate.SourceEntity{DisplayName: "Ezo Quarrington", CanonicalKey: "ezo quarrington", Source: "isotopes.example.com"}

	cases := []struct {
		name        string
		proposal    dictionaryupdate.Proposal
		hasConflict bool
		want        bool
	}{
		{"safe: full name, profile url, no conflict", dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, Category: "active_roster_core", Observed: fullEntity}, false, true},
		{"unsafe: not AddEntity", dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalUpdatePosition, Observed: fullEntity}, false, false},
		{"unsafe: initials-only name", dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, Category: "active_roster_core", Observed: initialsOnly}, false, false},
		{"unsafe: no profile url", dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, Category: "active_roster_core", Observed: noProfile}, false, false},
		{"unsafe: has a conflict", dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, Category: "active_roster_core", Observed: fullEntity}, true, false},
		{"unsafe: unrecognized category", dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, Category: "made_up_category", Observed: fullEntity}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dictionaryupdate.IsSafeAddition(tc.proposal, tc.hasConflict); got != tc.want {
				t.Errorf("IsSafeAddition() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWriteCandidate_ToolNameDefaultAndOverride(t *testing.T) {
	validDecision := dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3}
	current := dictionary.Dictionary{Version: "1.0.0", Categories: []dictionary.Category{{Name: "official_names", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes"}}}}, Decision: validDecision}
	write := func() dictionary.Dictionary {
		t.Helper()
		got, err := dictionaryupdate.WriteCandidate(current, current, 1, nil, "", time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
			func(dictionary.Dictionary) error { return nil },
			func(dictionary.Dictionary) error { return nil },
		)
		if err != nil {
			t.Fatalf("WriteCandidate: %v", err)
		}
		return got
	}
	t.Cleanup(func() { dictionaryupdate.SetToolName("") })

	if dictionaryupdate.DefaultToolName != "phins-news-engine-tools dictionary update" {
		t.Fatalf("DefaultToolName = %q", dictionaryupdate.DefaultToolName)
	}
	if got := write().Tool; got != dictionaryupdate.DefaultToolName {
		t.Fatalf("default Tool = %q, want %q", got, dictionaryupdate.DefaultToolName)
	}
	dictionaryupdate.SetToolName("host-tools dictionary update")
	if got := write().Tool; got != "host-tools dictionary update" {
		t.Fatalf("overridden Tool = %q, want host-tools dictionary update", got)
	}
	dictionaryupdate.SetToolName("")
	if got := dictionaryupdate.ToolName(); got != dictionaryupdate.DefaultToolName {
		t.Fatalf("ToolName after reset = %q, want default", got)
	}
}
