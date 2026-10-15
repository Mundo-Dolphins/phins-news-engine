package clustering_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// testConfig returns a Config with a full set of weights/thresholds
// representative of what Task 3's YAML loader is expected to produce.
// Values are chosen so that a "same event, different wording" pair (every
// applicable signal matches except same-source corroboration/novelty)
// clears JoinThreshold, and so AmbiguousLow < AmbiguousHigh < JoinThreshold
// as documented on Config.AmbiguousHigh.
func testConfig() clustering.Config {
	return clustering.Config{
		JoinThreshold: 0.70,
		JoinMargin:    0.15,
		AmbiguousLow:  0.35,
		AmbiguousHigh: 0.55,

		EntityMatchWeight:     0.30,
		ActionMatchWeight:     0.20,
		EventTypeMatchWeight:  0.20,
		OpponentMatchWeight:   0.10,
		DateWindowMatchWeight: 0.10,
		SourcePriorityWeight:  0.05,
		NoveltyMatchWeight:    0.05,

		RulesVersion: "test-rules-v1",
	}
}

var fixedNow = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

func baseRepresentation(articleID, sourceID, primaryEntity string, eventType eventsignals.EventType, action eventsignals.Action, eventDate time.Time) eventsignals.Representation {
	return eventsignals.Representation{
		ArticleID:     articleID,
		SourceID:      sourceID,
		PrimaryEntity: primaryEntity,
		EventType:     eventType,
		Action:        action,
		EventDate:     eventDate,
		EventWindow:   48 * time.Hour,
	}
}

func TestDecideJoin_SamePlayerDifferentEvents_DoesNotJoin(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Tavion Dale", eventsignals.EventTypeTransaction, eventsignals.ActionSign, day)
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Tavion Dale", eventsignals.EventTypeInjury, eventsignals.ActionInjure, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Fatalf("Action = %v, want ActionCreate (same entity, conflicting action/event type must not join): %+v", got.Action, got)
	}
	if got.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty on create", got.ClusterID)
	}
	if got.Score != 0 {
		t.Errorf("Score = %v, want 0 (conflict gate disqualifies the candidate entirely)", got.Score)
	}
	if len(got.Evidence) != 1 || !strings.Contains(string(got.Evidence[0]), "conflict:") ||
		!strings.Contains(string(got.Evidence[0]), string(eventsignals.EventTypeTransaction)) ||
		!strings.Contains(string(got.Evidence[0]), string(eventsignals.EventTypeInjury)) {
		t.Errorf("Evidence = %+v, want one conflict line naming both event types (transaction vs injury) — a shared entity alone must not join", got.Evidence)
	}
}

func TestDecideJoin_SameEventDifferentWording_Joins(t *testing.T) {
	day := fixedNow

	// Two representations describing the same contract extension; in
	// practice these would come from two differently-worded articles, but
	// eventsignals.Extract already abstracts wording away into these
	// signals, so the test only needs to construct the two
	// Representations directly.
	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day)
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin: %+v", got.Action, got)
	}
	if got.ClusterID != "cluster-1" {
		t.Errorf("ClusterID = %q, want cluster-1", got.ClusterID)
	}
	if got.MemberRole != clustering.MemberRoleConfirmation {
		t.Errorf("MemberRole = %v, want MemberRoleConfirmation (independent corroboration of the same fact)", got.MemberRole)
	}
	if len(got.Evidence) == 0 {
		t.Errorf("Evidence is empty, want at least one matched-signal explanation")
	}
}

func TestDecideJoin_AmbiguousAlternatives(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day)
	candidates := []clustering.Candidate{
		{
			ArticleID:      "existing-1",
			ClusterID:      "cluster-1",
			Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		},
		{
			ArticleID:      "existing-2",
			ClusterID:      "cluster-2",
			Representation: baseRepresentation("existing-2", "source-c", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		},
	}

	got := clustering.DecideJoin(incoming, candidates, testConfig(), fixedNow)

	if got.Action != clustering.ActionAmbiguous {
		t.Fatalf("Action = %v, want ActionAmbiguous (two equally strong candidate clusters): %+v", got.Action, got)
	}
	if got.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty on ambiguous", got.ClusterID)
	}
	if len(got.Alternatives) != 1 {
		t.Fatalf("Alternatives = %+v, want exactly 1 (the runner-up cluster)", got.Alternatives)
	}
	if diff := got.Score - got.Alternatives[0].Score; diff < 0 || diff >= testConfig().JoinMargin {
		t.Errorf("Score - Alternatives[0].Score = %v, want within JoinMargin (that's why it's ambiguous)", diff)
	}
}

func TestDecideJoin_ConfirmationRole(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Jaylen Wickersham", eventsignals.EventTypeConfirmation, eventsignals.ActionConfirm, day)
	incoming.ContentSignal = eventsignals.ContentSignalConfirmation

	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Jaylen Wickersham", eventsignals.EventTypeRumor, eventsignals.ActionReport, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin (confirmation of an earlier rumor is the same event): %+v", got.Action, got)
	}
	if got.MemberRole != clustering.MemberRoleConfirmation {
		t.Errorf("MemberRole = %v, want MemberRoleConfirmation", got.MemberRole)
	}
}

func TestDecideJoin_CorrectionRole(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Jaylen Wickersham", eventsignals.EventTypeCorrection, eventsignals.ActionCorrect, day)
	incoming.ContentSignal = eventsignals.ContentSignalCorrection

	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Jaylen Wickersham", eventsignals.EventTypeTransaction, eventsignals.ActionSign, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin (correction of an earlier report is the same event): %+v", got.Action, got)
	}
	if got.MemberRole != clustering.MemberRoleCorrection {
		t.Errorf("MemberRole = %v, want MemberRoleCorrection", got.MemberRole)
	}
}

func TestDecideJoin_SubstantialUpdateRole(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionSign, day)
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin: %+v", got.Action, got)
	}
	if got.MemberRole != clustering.MemberRoleSubstantialUpdate {
		t.Errorf("MemberRole = %v, want MemberRoleSubstantialUpdate (action progressed from extend to sign on the same event)", got.MemberRole)
	}
}

func TestDecideJoin_CreatesWhenNoCandidateReachesAmbiguousLow(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Jaylen Wickersham", eventsignals.EventTypeTransaction, eventsignals.ActionSign, day)
	// Different primary entity: conflict gate fires, score forced to 0.
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Tavion Dale", eventsignals.EventTypeTransaction, eventsignals.ActionSign, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Errorf("Action = %v, want ActionCreate", got.Action)
	}
}

func TestDecideJoin_CreatesWhenNoCandidates(t *testing.T) {
	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionSign, fixedNow)

	got := clustering.DecideJoin(incoming, nil, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Errorf("Action = %v, want ActionCreate", got.Action)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("Alternatives = %+v, want empty", got.Alternatives)
	}
}

func TestDecideJoin_SkipsCandidateWithEmptyClusterID(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day)
	candidates := []clustering.Candidate{
		{
			ArticleID:      "existing-1",
			ClusterID:      "", // crash-recovery edge case, see ADR 0009
			Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		},
	}

	got := clustering.DecideJoin(incoming, candidates, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Errorf("Action = %v, want ActionCreate (only qualifying candidate has an empty ClusterID)", got.Action)
	}
	if got.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty", got.ClusterID)
	}
}

func TestDecideJoin_TreatsNearCopyAsEvidenceNotRejection(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day)
	candidate := clustering.Candidate{
		ArticleID:         "existing-1",
		ClusterID:         "cluster-1",
		Representation:    baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		DuplicateRelation: clustering.RelationNearCopy,
		DuplicateScore:    0.91,
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	// clustering must never reject; a near-copy dedup hint still produces
	// a join (deduplication decides rejection separately, upstream).
	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin (clustering never rejects)", got.Action)
	}
	if got.MemberRole != clustering.MemberRoleDuplicate {
		t.Errorf("MemberRole = %v, want MemberRoleDuplicate", got.MemberRole)
	}
	found := false
	for _, e := range got.Evidence {
		if e == "dedup: candidate flagged near_copy (score 0.91)" {
			found = true
		}
	}
	if !found {
		t.Errorf("Evidence = %+v, want a dedup near_copy evidence line", got.Evidence)
	}
}

func TestDecideJoin_IsPureAndDeterministic(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day)
	candidates := []clustering.Candidate{
		{
			ArticleID:      "existing-1",
			ClusterID:      "cluster-1",
			Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		},
		{
			ArticleID:      "existing-2",
			ClusterID:      "cluster-2",
			Representation: baseRepresentation("existing-2", "source-c", "Tavion Dale", eventsignals.EventTypeInjury, eventsignals.ActionInjure, day),
		},
	}
	cfg := testConfig()

	first := clustering.DecideJoin(incoming, candidates, cfg, fixedNow)
	second := clustering.DecideJoin(incoming, candidates, cfg, fixedNow)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("DecideJoin is not deterministic: first = %+v, second = %+v", first, second)
	}
}

func TestCompare_ConflictingDateWindowDisqualifies(t *testing.T) {
	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, fixedNow)
	farAway := baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, fixedNow.Add(-30*24*time.Hour))

	cmp := clustering.Compare(incoming, clustering.Candidate{ArticleID: "existing-1", ClusterID: "cluster-1", Representation: farAway}, testConfig())

	if cmp.Relation != clustering.RelationDifferent {
		t.Errorf("Relation = %v, want RelationDifferent", cmp.Relation)
	}
	if cmp.Score != 0 {
		t.Errorf("Score = %v, want 0", cmp.Score)
	}
}

// --- Regression tests: code review findings on this package (Task 5 fix
// pass) ---

// Finding 1: EntityMatchWeight's applicability check only looked at the
// incoming article's side, unlike every other two-sided signal in Compare
// (OpponentMatchWeight, SourcePriorityWeight, NoveltyMatchWeight). Once
// candidates carry sparse, real persisted Representations (Task 7), a
// candidate with no PrimaryEntity opinion would score as a NON-MATCH
// instead of being excluded from scoring like the package's documented
// design principle requires (clustering.go's Config doc comment: "a signal
// with no data on either side ... is excluded from both the numerator and
// the denominator rather than counted as a non-match").
func TestCompare_EntitySignalExcludedWhenOnlyOneSideHasOpinion(t *testing.T) {
	day := fixedNow
	cfg := testConfig()

	// Baseline: neither side has a PrimaryEntity opinion — the
	// already-symmetric "no data on either side" case that
	// OpponentMatchWeight/SourcePriorityWeight/NoveltyMatchWeight already
	// handle correctly. Compare must treat the fixed scenario identically.
	noOpinion := clustering.Compare(
		baseRepresentation("incoming", "source-a", "", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		clustering.Candidate{
			ArticleID:      "existing-1",
			ClusterID:      "cluster-1",
			Representation: baseRepresentation("existing-1", "source-b", "", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		},
		cfg,
	)

	// Regression case: incoming has an opinion (non-empty PrimaryEntity)
	// but the candidate's persisted Representation does not — exactly the
	// sparse-representation shape Task 7 will produce. Before the fix this
	// counted as a non-match (EntityMatchWeight added to the denominator,
	// not the numerator), scoring lower than the symmetric baseline above
	// even though both cases are "one side has no opinion on entity."
	oneSided := clustering.Compare(
		baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		clustering.Candidate{
			ArticleID:      "existing-1",
			ClusterID:      "cluster-1",
			Representation: baseRepresentation("existing-1", "source-b", "", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		},
		cfg,
	)

	if oneSided.EntityMatch {
		t.Errorf("EntityMatch = true, want false (candidate side has no PrimaryEntity opinion)")
	}
	if oneSided.Score != noOpinion.Score {
		t.Errorf("Score = %v (one-sided PrimaryEntity), want %v (equal to the both-empty baseline: the signal must be excluded from scoring, not penalized as a non-match)",
			oneSided.Score, noOpinion.Score)
	}
}

// Finding 1, ActionMatchWeight half: same bug, same fix, on the Action
// signal instead of PrimaryEntity.
func TestCompare_ActionSignalExcludedWhenOnlyOneSideHasOpinion(t *testing.T) {
	day := fixedNow
	cfg := testConfig()

	noOpinion := clustering.Compare(
		baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.Action(""), day),
		clustering.Candidate{
			ArticleID:      "existing-1",
			ClusterID:      "cluster-1",
			Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.Action(""), day),
		},
		cfg,
	)

	// Incoming names an Action; the candidate's persisted Representation
	// does not. Before the fix, ActionMatchWeight counted this as a
	// non-match instead of excluding it.
	oneSided := clustering.Compare(
		baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day),
		clustering.Candidate{
			ArticleID:      "existing-1",
			ClusterID:      "cluster-1",
			Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.Action(""), day),
		},
		cfg,
	)

	if oneSided.ActionMatch {
		t.Errorf("ActionMatch = true, want false (candidate side has no Action opinion)")
	}
	if oneSided.Score != noOpinion.Score {
		t.Errorf("Score = %v (one-sided Action), want %v (equal to the both-empty baseline: the signal must be excluded from scoring, not penalized as a non-match)",
			oneSided.Score, noOpinion.Score)
	}
}

// Finding 2: detectConflict and Compare duplicated the same window/diff
// arithmetic, and because detectConflict's gate runs first, dateWindowMatched
// is structurally guaranteed true on every Comparison that survives it —
// DateWindowMatchWeight could never actually discriminate among candidates,
// only dilute the other six signals. This test proves the fixed behavior:
// Score is identical regardless of DateWindowMatchWeight's value, while
// DateWindowMatch itself is always true post-gate and the weight still
// toggles whether the evidence line appears (matching the "0 disables this
// signal" convention documented for every other weight).
func TestCompare_DateWindowMatchIsAlwaysTruePostGateAndExcludedFromScore(t *testing.T) {
	day := fixedNow
	incoming := baseRepresentation("incoming", "source-a", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day)
	// Within the gate's 48h window but not exactly equal, so this isn't a
	// degenerate zero-diff case.
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Ezo Quarrington", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, day.Add(2*time.Hour)),
	}

	cfgWeighted := testConfig()
	cfgWeighted.DateWindowMatchWeight = 0.50 // deliberately large

	cfgUnweighted := testConfig()
	cfgUnweighted.DateWindowMatchWeight = 0

	withWeight := clustering.Compare(incoming, candidate, cfgWeighted)
	withoutWeight := clustering.Compare(incoming, candidate, cfgUnweighted)

	if !withWeight.DateWindowMatch {
		t.Errorf("DateWindowMatch = false, want true (the gate already guarantees a surviving comparison is within window)")
	}
	if !withoutWeight.DateWindowMatch {
		t.Errorf("DateWindowMatch = false, want true regardless of DateWindowMatchWeight")
	}
	if withWeight.Score != withoutWeight.Score {
		t.Errorf("Score = %v with DateWindowMatchWeight=0.50 vs %v with DateWindowMatchWeight=0, want equal — the weight must not affect Score since it can never discriminate among survivors",
			withWeight.Score, withoutWeight.Score)
	}

	wantEvidence := clustering.Evidence("date window: event dates within 48h0m0s")
	foundWithWeight := false
	for _, e := range withWeight.Evidence {
		if e == wantEvidence {
			foundWithWeight = true
		}
	}
	if !foundWithWeight {
		t.Errorf("Evidence = %+v, want a date window evidence line when DateWindowMatchWeight > 0", withWeight.Evidence)
	}
	for _, e := range withoutWeight.Evidence {
		if e == wantEvidence {
			t.Errorf("Evidence = %+v, want no date window evidence line when DateWindowMatchWeight == 0", withoutWeight.Evidence)
		}
	}
}

// --- Task 11: required test scenarios (plan audit) ---
//
// The tests below fill scenario/assertion gaps identified while auditing
// existing coverage against the plan's 8 required scenarios (see
// task-11-report.md): "same player different events" and "aggregator vs
// primary source" were already well covered above and in
// representative_test.go; "injury with meaningful updates", "game preview
// vs result", "repeated quotes", and "recycled article days later" had no
// dedicated domain-level test before this task.

// Scenario: an injury report followed by a meaningful update (a return to
// practice) is the same event — action progressing from injure to return
// is a substantial update, not a new story.
func TestDecideJoin_InjuryMeaningfulUpdate_JoinsAsSubstantialUpdate(t *testing.T) {
	firstReport := fixedNow
	update := fixedNow.Add(20 * time.Hour) // still within the 48h EventWindow baseRepresentation sets

	incoming := baseRepresentation("incoming", "source-a", "Jaylen Wickersham", eventsignals.EventTypeInjury, eventsignals.ActionReturn, update)
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Jaylen Wickersham", eventsignals.EventTypeInjury, eventsignals.ActionInjure, firstReport),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin (a return-to-practice update is the same injury event): %+v", got.Action, got)
	}
	if got.ClusterID != "cluster-1" {
		t.Errorf("ClusterID = %q, want cluster-1", got.ClusterID)
	}
	if got.MemberRole != clustering.MemberRoleSubstantialUpdate {
		t.Errorf("MemberRole = %v, want MemberRoleSubstantialUpdate (action progressed from injure to return)", got.MemberRole)
	}
	found := false
	for _, e := range got.Evidence {
		if strings.Contains(string(e), "Jaylen Wickersham") {
			found = true
		}
	}
	if !found {
		t.Errorf("Evidence = %+v, want an entity-match line naming Jaylen Wickersham", got.Evidence)
	}
}

// Scenario: a game preview and that same game's result recap are separate
// events unless rules explicitly relate them. Nothing in eventTypeCompatible
// treats "game" as a referential EventType (unlike
// confirmation/correction/rumor/analysis/reaction/quote), so the two are
// only ever the same cluster if they fall within the "game" EventType's
// configured window — here they do not (72h apart vs a 24h window), so the
// date-window conflict gate must hard-disqualify the pair regardless of
// sharing an entity, matching testdata/clustering_groups.yml's
// game-preview/game-result gold groups.
func TestDecideJoin_GamePreviewAndGameResult_DoesNotJoinAcrossWindow(t *testing.T) {
	previewDate := fixedNow
	resultDate := fixedNow.Add(72 * time.Hour)

	incoming := eventsignals.Representation{
		ArticleID: "incoming", SourceID: "source-a",
		EventType: eventsignals.EventTypeGame, Action: eventsignals.ActionResult,
		EventDate: resultDate, EventWindow: 24 * time.Hour,
	}
	candidate := clustering.Candidate{
		ArticleID: "existing-1", ClusterID: "cluster-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "source-b",
			EventType: eventsignals.EventTypeGame, Action: eventsignals.ActionPreview,
			EventDate: previewDate, EventWindow: 24 * time.Hour,
		},
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Fatalf("Action = %v, want ActionCreate (preview and result are separate unless rules explicitly relate them): %+v", got.Action, got)
	}
	if got.Score != 0 {
		t.Errorf("Score = %v, want 0 (date-window conflict disqualifies the pair)", got.Score)
	}
	if len(got.Evidence) != 1 || !strings.Contains(string(got.Evidence[0]), "conflict:") || !strings.Contains(string(got.Evidence[0]), "24h0m0s") {
		t.Errorf("Evidence = %+v, want one conflict line citing the 24h game event window", got.Evidence)
	}
}

// Scenario: repeated declarations/quotes from the same press conference are
// one event — a second outlet quoting the same remarks is corroboration,
// not a new story.
func TestDecideJoin_RepeatedQuotes_JoinsAsConfirmation(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Mike Thistlewood", eventsignals.EventTypeQuote, eventsignals.ActionQuote, day.Add(2*time.Hour))
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Mike Thistlewood", eventsignals.EventTypeQuote, eventsignals.ActionQuote, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin (a second outlet quoting the same remarks is the same event): %+v", got.Action, got)
	}
	if got.MemberRole != clustering.MemberRoleConfirmation {
		t.Errorf("MemberRole = %v, want MemberRoleConfirmation (independent corroboration of the same quote, no action progression)", got.MemberRole)
	}
	var sawEntity, sawAction bool
	for _, e := range got.Evidence {
		if strings.Contains(string(e), "Mike Thistlewood") {
			sawEntity = true
		}
		if strings.Contains(string(e), `action "quote"`) {
			sawAction = true
		}
	}
	if !sawEntity || !sawAction {
		t.Errorf("Evidence = %+v, want lines naming Mike Thistlewood and the matched quote action", got.Evidence)
	}
}

// Scenario: a recycled article republished days later, well outside the
// event type's window, must NOT be bridged back to the original cluster
// just because the wording (and every event signal) is identical —
// deduplication's job, not clustering's (see
// testdata/clustering_groups.yml's recycled-original/recycled-later gold
// groups, 10 days apart).
func TestDecideJoin_RecycledArticleDaysLater_DoesNotJoin(t *testing.T) {
	original := fixedNow
	recycled := fixedNow.Add(10 * 24 * time.Hour)

	incoming := baseRepresentation("incoming", "source-a", "Zeb Holloran", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, recycled)
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Zeb Holloran", eventsignals.EventTypeTransaction, eventsignals.ActionExtend, original),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Fatalf("Action = %v, want ActionCreate (10 days apart is outside the transaction event window, even with identical entity/action): %+v", got.Action, got)
	}
	if got.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty", got.ClusterID)
	}
	if len(got.Evidence) != 1 || !strings.Contains(string(got.Evidence[0]), "conflict:") || !strings.Contains(string(got.Evidence[0]), "240h0m0s") {
		t.Errorf("Evidence = %+v, want one conflict line citing the 240h (10 day) gap outside the transaction window", got.Evidence)
	}
}

// --- Known limitations (ADR 0013, "Known limitations" #3): pinned as
// regression tests documenting the CURRENT (gap) behavior, per Task 11's
// Option A. Both are calibration gaps in already-approved eventsignals/
// clustering vocabulary/interaction, discovered by Task 9's offline
// evaluation dataset, deliberately left unfixed pending a future
// rules-tuning pass (see this task's report for the Option A vs. B
// reasoning). If either gap is ever closed, the assertion pinned as
// "CURRENT (gap) behavior" below must change, and this comment/test should
// be updated or removed accordingly — that is the point of pinning it now
// rather than leaving it untested.

// Finding 1 regression: a team name is routing context, not enough event
// identity to merge two otherwise generic stories.
func TestDecideJoin_TeamLevelGenericStoriesCreateSeparateClusters(t *testing.T) {
	day := fixedNow

	// Two unrelated "Isotopes" headlines: neither names a player, opponent,
	// or action — in production these would arise from detectEntities'
	// team-level PrimaryEntity fallback firing on two unrelated stories that
	// each only mention the team name.
	incoming := baseRepresentation("incoming", "source-a", "Springfield Isotopes", eventsignals.EventTypeGeneric, "", day)
	incoming.PrimaryTeam = "Springfield Isotopes"
	candidate := clustering.Candidate{
		ArticleID: "existing-1",
		ClusterID: "cluster-1",
		Representation: baseRepresentation(
			"existing-1", "source-b", "Springfield Isotopes", eventsignals.EventTypeGeneric, "", day.Add(1*time.Hour),
		),
	}
	candidate.Representation.PrimaryTeam = "Springfield Isotopes"

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Fatalf("Action = %v, want ActionCreate: shared team-level generic context must not merge unrelated stories", got.Action)
	}
	if got.Score != 0 {
		t.Errorf("Score = %v, want 0 for insufficient event identity", got.Score)
	}
	if len(got.Evidence) != 1 || !strings.Contains(string(got.Evidence[0]), "insufficient event identity") {
		t.Errorf("Evidence = %+v, want an insufficient-event-identity explanation", got.Evidence)
	}
}

func TestDecideJoin_TeamLevelGenericNearCopyCanStillJoin(t *testing.T) {
	day := fixedNow
	incoming := baseRepresentation("incoming", "source-a", "Springfield Isotopes", eventsignals.EventTypeGeneric, "", day)
	incoming.PrimaryTeam = "Springfield Isotopes"
	candidateRep := baseRepresentation("existing-1", "source-b", "Springfield Isotopes", eventsignals.EventTypeGeneric, "", day.Add(time.Hour))
	candidateRep.PrimaryTeam = "Springfield Isotopes"

	got := clustering.DecideJoin(incoming, []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-1", Representation: candidateRep,
		DuplicateRelation: clustering.RelationNearCopy, DuplicateScore: 0.92,
	}}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin when near-copy evidence supplies event identity: %+v", got.Action, got)
	}
}

func TestDecideJoin_PlayerLevelGenericStoriesCreateSeparateClusters(t *testing.T) {
	day := fixedNow
	incoming := baseRepresentation("incoming", "source-a", "Ansel Wilkinsby", eventsignals.EventTypeGeneric, "", day)
	incoming.PrimaryTeam = "Springfield Isotopes"
	candidateRep := baseRepresentation("existing-1", "source-b", "Ansel Wilkinsby", eventsignals.EventTypeGeneric, "", day.Add(time.Hour))
	candidateRep.PrimaryTeam = "Springfield Isotopes"

	got := clustering.DecideJoin(incoming, []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-1", Representation: candidateRep,
	}}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Fatalf("Action = %v, want ActionCreate: a shared player without an event action is not enough identity", got.Action)
	}
	if got.Score != 0 {
		t.Errorf("Score = %v, want 0 for insufficient event identity", got.Score)
	}
}

// Scenario: two unrelated articles about the same player each happen to
// contain reported speech, so eventsignals classifies both as EventTypeQuote
// even though their actions (and topics) differ — e.g. one about a
// game-status update, the other about an injury. A shared "quote" framing is
// not evidence they cover the same incident, so this must not join purely
// because the coarse EventType matches; regression test for a production
// over-merge where sparse same-EventType-different-action pairs scored high
// enough to join.
func TestDecideJoin_SamePlayerDifferentQuoteTopics_DoesNotJoin(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Ansel Wilkinsby", eventsignals.EventTypeQuote, eventsignals.ActionGameStatus, day)
	incoming.PrimaryTeam = "Springfield Isotopes"
	candidateRep := baseRepresentation("existing-1", "source-b", "Ansel Wilkinsby", eventsignals.EventTypeQuote, eventsignals.ActionInjure, day.Add(time.Hour))
	candidateRep.PrimaryTeam = "Springfield Isotopes"

	got := clustering.DecideJoin(incoming, []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-1", Representation: candidateRep,
	}}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Fatalf("Action = %v, want ActionCreate: a shared player and a shared referential EventType (quote) without a matching action is not enough identity", got.Action)
	}
	if got.Score != 0 {
		t.Errorf("Score = %v, want 0 for insufficient event identity", got.Score)
	}
}

func TestDecideJoin_TeamLevelGenericStoriesWithSameOpponentCreateSeparateClusters(t *testing.T) {
	day := fixedNow
	incoming := baseRepresentation("incoming", "source-a", "Springfield Isotopes", eventsignals.EventTypeGeneric, "", day)
	incoming.PrimaryTeam = "Springfield Isotopes"
	incoming.Opponent = "Harbor City 88ers"
	candidateRep := baseRepresentation("existing-1", "source-b", "Springfield Isotopes", eventsignals.EventTypeGeneric, "", day.Add(time.Hour))
	candidateRep.PrimaryTeam = "Springfield Isotopes"
	candidateRep.Opponent = "Harbor City 88ers"

	got := clustering.DecideJoin(incoming, []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-1", Representation: candidateRep,
	}}, testConfig(), fixedNow)

	if got.Action != clustering.ActionCreate {
		t.Fatalf("Action = %v, want ActionCreate: team and opponent alone do not identify one event story", got.Action)
	}
}

func TestDecideJoin_TeamLevelGamePreviewWithSameOpponentJoins(t *testing.T) {
	day := fixedNow
	incoming := baseRepresentation("incoming", "source-a", "Springfield Isotopes", eventsignals.EventTypeGame, eventsignals.ActionPreview, day)
	incoming.PrimaryTeam = "Springfield Isotopes"
	incoming.Opponent = "Harbor City 88ers"
	candidateRep := baseRepresentation("existing-1", "source-b", "Springfield Isotopes", eventsignals.EventTypeGame, eventsignals.ActionPreview, day.Add(time.Hour))
	candidateRep.PrimaryTeam = "Springfield Isotopes"
	candidateRep.Opponent = "Harbor City 88ers"

	got := clustering.DecideJoin(incoming, []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-1", Representation: candidateRep,
	}}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin for the same identified game preview: %+v", got.Action, got)
	}
}

func TestDecideJoin_BareConfirmActionJoinsOwnTransactionReport(t *testing.T) {
	day := fixedNow

	incoming := baseRepresentation("incoming", "source-a", "Bramwell Oxtoby", eventsignals.EventTypeConfirmation, eventsignals.ActionConfirm, day.Add(1*time.Hour))
	candidate := clustering.Candidate{
		ArticleID:      "existing-1",
		ClusterID:      "cluster-1",
		Representation: baseRepresentation("existing-1", "source-b", "Bramwell Oxtoby", eventsignals.EventTypeTransaction, eventsignals.ActionSign, day),
	}

	got := clustering.DecideJoin(incoming, []clustering.Candidate{candidate}, testConfig(), fixedNow)

	if got.Action != clustering.ActionJoin {
		t.Fatalf("Action = %v, want ActionJoin: %+v", got.Action, got)
	}
	if got.MemberRole != clustering.MemberRoleConfirmation {
		t.Errorf("MemberRole = %v, want confirmation", got.MemberRole)
	}
}
