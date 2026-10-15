package clustering_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// neutralEditorial is the placeholder editorial.Score every candidate in
// these tests carries, matching editorial.Evaluate's current always-neutral
// behavior (see editorial.Evaluate's doc comment).
var neutralEditorial = editorial.Score{Value: editorial.NeutralScore, Placeholder: true}

// testRepresentativeConfig returns a RepresentativeConfig with a full set of
// weights/thresholds representative of what a future YAML loader conversion
// (see clusteringrules.RepresentativeRankingConfig) is expected to produce.
// ClickbaitPenaltyWeight is set deliberately high (1.5, well above the 1.0
// ceiling every other weighted signal's contribution is bounded by) so the
// "clickbait can block even a high-priority domain" behavior is exercised
// unambiguously regardless of how the other weights are tuned later — a
// real rules file would likely use a smaller value.
func testRepresentativeConfig() clustering.RepresentativeConfig {
	return clustering.RepresentativeConfig{
		PrimarySourceWeight:   0.20,
		DomainPriorityWeight:  0.20,
		CompletenessWeight:    0.15,
		NoveltyWeight:         0.15,
		ClarityWeight:         0.10,
		FreshnessWeight:       0.10,
		OriginalContentWeight: 0.05,
		EditorialWeight:       0.03,
		VerifiabilityWeight:   0.02,

		ClickbaitPenaltyWeight: 1.5,

		DomainPriorities: map[string]float64{
			"isotopes.example.com":     95,
			"gridirontalk.example.com": 60,
		},
		DefaultDomainPriority: 40,

		FreshnessWindow: 24 * time.Hour,

		SubstantialUpdateThreshold: 0.5,
		RepresentativeSwitchMargin: 0.05,

		RulesVersion: "test-representative-rules-v1",
	}
}

func TestRankRepresentative_PrimarySourceBeatsAggregator_WhenFreshnessComparable(t *testing.T) {
	published := fixedNow.Add(-1 * time.Hour)

	primary := clustering.RepresentativeCandidate{
		ArticleID: "primary-1",
		Representation: eventsignals.Representation{
			ArticleID:     "primary-1",
			SourceID:      "team-pr",
			Domain:        "isotopes.example.com",
			PrimaryEntity: "Ezo Quarrington",
			EventType:     eventsignals.EventTypeTransaction,
			Action:        eventsignals.ActionSign,
			EventDate:     published,
			PublishedAt:   published,
		},
		IsPrimarySource: true,
		Editorial:       neutralEditorial,
	}
	aggregator := clustering.RepresentativeCandidate{
		ArticleID: "aggregator-1",
		Representation: eventsignals.Representation{
			ArticleID:     "aggregator-1",
			SourceID:      "some-blog",
			Domain:        "randomsportsblog.example",
			PrimaryEntity: "Ezo Quarrington",
			EventType:     eventsignals.EventTypeTransaction,
			Action:        eventsignals.ActionSign,
			EventDate:     published,
			PublishedAt:   published,
		},
		IsPrimarySource: false,
		Editorial:       neutralEditorial,
	}

	cfg := testRepresentativeConfig()
	// Input order deliberately does not match the expected output order, so
	// this test also proves RankRepresentative sorts rather than reflecting
	// input order.
	got := clustering.RankRepresentative([]clustering.RepresentativeCandidate{aggregator, primary}, cfg, fixedNow)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[0].ArticleID != "primary-1" {
		t.Fatalf("got[0].ArticleID = %q, want primary-1 (primary/official source on a higher-priority domain must rank first when freshness is comparable): %+v", got[0].ArticleID, got)
	}
	if got[0].Score <= got[1].Score {
		t.Errorf("got[0].Score = %v, got[1].Score = %v, want primary strictly higher", got[0].Score, got[1].Score)
	}
	if got[0].RulesVersion != cfg.RulesVersion {
		t.Errorf("RulesVersion = %q, want %q", got[0].RulesVersion, cfg.RulesVersion)
	}
	if len(got[0].Evidence) == 0 {
		t.Errorf("Evidence is empty, want an explanation for the winning score")
	}
}

func TestRankRepresentative_ClickbaitPenaltyBlocksHighPriorityDomain(t *testing.T) {
	published := fixedNow.Add(-1 * time.Hour)

	sensational := clustering.RepresentativeCandidate{
		ArticleID: "sensational-1",
		Representation: eventsignals.Representation{
			ArticleID:     "sensational-1",
			SourceID:      "team-pr",
			Domain:        "isotopes.example.com", // the highest-priority domain in testRepresentativeConfig
			PrimaryEntity: "Ezo Quarrington",
			EventType:     eventsignals.EventTypeTransaction,
			Action:        eventsignals.ActionSign,
			EventDate:     published,
			PublishedAt:   published,
		},
		IsPrimarySource:  true,
		ClickbaitPenalty: 1.0,
		Editorial:        neutralEditorial,
	}
	modest := clustering.RepresentativeCandidate{
		ArticleID: "modest-1",
		Representation: eventsignals.Representation{
			ArticleID:     "modest-1",
			SourceID:      "some-blog",
			Domain:        "gridirontalk.example.com",
			PrimaryEntity: "Ezo Quarrington",
			EventType:     eventsignals.EventTypeTransaction,
			Action:        eventsignals.ActionSign,
			EventDate:     published,
			PublishedAt:   published,
		},
		IsPrimarySource:  false,
		ClickbaitPenalty: 0,
		Editorial:        neutralEditorial,
	}

	got := clustering.RankRepresentative([]clustering.RepresentativeCandidate{sensational, modest}, testRepresentativeConfig(), fixedNow)

	if got[0].ArticleID != "modest-1" {
		t.Fatalf("got[0].ArticleID = %q, want modest-1 (a maximal clickbait penalty must be able to overcome even the highest-priority domain): %+v", got[0].ArticleID, got)
	}
	if got[1].Score != 0 {
		t.Errorf("sensational-1's Score = %v, want 0 (fully floored by the clickbait penalty, never negative)", got[1].Score)
	}
}

func TestReplacesCurrent_SubstantialUpdateExceedsSwitchMargin_Replaces(t *testing.T) {
	firstReportTime := fixedNow.Add(-3 * time.Hour)
	updateTime := fixedNow.Add(-1 * time.Hour)

	current := clustering.RepresentativeCandidate{
		ArticleID: "first-report",
		Representation: eventsignals.Representation{
			ArticleID:     "first-report",
			SourceID:      "source-a",
			Domain:        "gridirontalk.example.com",
			PrimaryEntity: "Ezo Quarrington",
			EventType:     eventsignals.EventTypeRumor,
			ContentSignal: eventsignals.ContentSignalRumor,
			EventDate:     firstReportTime,
			PublishedAt:   firstReportTime,
		},
		Editorial: neutralEditorial,
	}

	substantialUpdate := clustering.RepresentativeCandidate{
		ArticleID: "update-1",
		Representation: eventsignals.Representation{
			ArticleID:     "update-1",
			SourceID:      "source-b",
			Domain:        "isotopes.example.com",
			PrimaryEntity: "Ezo Quarrington",
			Competition:   "NFL",
			EventType:     eventsignals.EventTypeTransaction,
			Action:        eventsignals.ActionSign,
			EventDate:     updateTime,
			PublishedAt:   updateTime,
		},
		IsPrimarySource: true,
		Editorial:       neutralEditorial,
	}

	replaces, score := clustering.ReplacesCurrent(current, substantialUpdate, testRepresentativeConfig(), fixedNow)

	if !replaces {
		t.Fatalf("ReplacesCurrent = false, want true (action progressed from rumor to signed, with richer completeness and a higher-priority primary source, clearing both SubstantialUpdateThreshold and RepresentativeSwitchMargin)")
	}
	if score.ArticleID != "update-1" {
		t.Errorf("score.ArticleID = %q, want update-1", score.ArticleID)
	}
	if score.RulesVersion != testRepresentativeConfig().RulesVersion {
		t.Errorf("score.RulesVersion = %q, want %q", score.RulesVersion, testRepresentativeConfig().RulesVersion)
	}
}

func TestReplacesCurrent_MinorRestatement_DoesNotReplace(t *testing.T) {
	firstReportTime := fixedNow.Add(-3 * time.Hour)
	restateTime := fixedNow.Add(-2 * time.Hour)

	rep := eventsignals.Representation{
		SourceID:      "source-a",
		Domain:        "gridirontalk.example.com",
		PrimaryEntity: "Ezo Quarrington",
		EventType:     eventsignals.EventTypeTransaction,
		Action:        eventsignals.ActionSign,
		EventDate:     firstReportTime,
		PublishedAt:   firstReportTime,
	}
	current := clustering.RepresentativeCandidate{
		ArticleID:      "first-report",
		Representation: rep,
		Editorial:      neutralEditorial,
	}

	restatement := rep
	restatement.ArticleID = "restate-1"
	restatement.PublishedAt = restateTime
	restatement.EventDate = restateTime
	candidate := clustering.RepresentativeCandidate{
		ArticleID:      "restate-1",
		Representation: restatement,
		Editorial:      neutralEditorial,
	}

	replaces, _ := clustering.ReplacesCurrent(current, candidate, testRepresentativeConfig(), fixedNow)

	if replaces {
		t.Errorf("ReplacesCurrent = true, want false (identical event signal restated by the same source, no novelty/completeness gain, must not replace)")
	}
}

// --- Task 11: required test scenarios (plan audit) ---

// Scenario: a transaction reported by multiple independent sources — here
// an official announcement plus two later confirmations — should all join
// one cluster (see clustering_test.go's join-decision tests), with the
// official/primary source winning representative selection over both
// confirmations, not merely over a single aggregator.
func TestRankRepresentative_TransactionMultiSource_OfficialSourceOutranksMultipleConfirmations(t *testing.T) {
	published := fixedNow.Add(-2 * time.Hour)

	official := clustering.RepresentativeCandidate{
		ArticleID: "official-1",
		Representation: eventsignals.Representation{
			ArticleID:     "official-1",
			SourceID:      "team-pr",
			Domain:        "isotopes.example.com",
			PrimaryEntity: "Bramwell Oxtoby",
			EventType:     eventsignals.EventTypeTransaction,
			Action:        eventsignals.ActionSign,
			EventDate:     published,
			PublishedAt:   published,
		},
		IsPrimarySource: true,
		Editorial:       neutralEditorial,
	}
	confirmation1 := clustering.RepresentativeCandidate{
		ArticleID: "confirm-1",
		Representation: eventsignals.Representation{
			ArticleID:     "confirm-1",
			SourceID:      "rumor-outlet",
			Domain:        "gridironrumors.example.com",
			PrimaryEntity: "Bramwell Oxtoby",
			EventType:     eventsignals.EventTypeConfirmation,
			ContentSignal: eventsignals.ContentSignalConfirmation,
			Action:        eventsignals.ActionConfirm,
			EventDate:     published.Add(1 * time.Hour),
			PublishedAt:   published.Add(1 * time.Hour),
		},
		Editorial: neutralEditorial,
	}
	confirmation2 := clustering.RepresentativeCandidate{
		ArticleID: "confirm-2",
		Representation: eventsignals.Representation{
			ArticleID:     "confirm-2",
			SourceID:      "espn",
			Domain:        "sportsnet.example.com",
			PrimaryEntity: "Bramwell Oxtoby",
			EventType:     eventsignals.EventTypeConfirmation,
			ContentSignal: eventsignals.ContentSignalConfirmation,
			Action:        eventsignals.ActionConfirm,
			EventDate:     published.Add(2 * time.Hour),
			PublishedAt:   published.Add(2 * time.Hour),
		},
		Editorial: neutralEditorial,
	}

	// Input order deliberately does not match the expected output order.
	got := clustering.RankRepresentative(
		[]clustering.RepresentativeCandidate{confirmation2, confirmation1, official},
		testRepresentativeConfig(), fixedNow,
	)

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	if got[0].ArticleID != "official-1" {
		t.Fatalf("got[0].ArticleID = %q, want official-1 (the official source must outrank both confirmations, not just a single aggregator): %+v", got[0].ArticleID, got)
	}
	if got[0].Score <= got[1].Score || got[1].Score <= 0 {
		t.Errorf("got scores = [%v, %v, %v], want strictly descending", got[0].Score, got[1].Score, got[2].Score)
	}
	foundPrimarySourceEvidence := false
	for _, e := range got[0].Evidence {
		if e == "primary source: true" {
			foundPrimarySourceEvidence = true
		}
	}
	if !foundPrimarySourceEvidence {
		t.Errorf("winner's Evidence = %+v, want a %q line", got[0].Evidence, "primary source: true")
	}
}

// Scenario: an injury report followed by a meaningful update (a return to
// practice, naming the upcoming opponent) should replace the first report as
// representative — the update contributes real novelty and completeness
// gain, from a higher-priority (official team) domain.
func TestReplacesCurrent_InjuryMeaningfulUpdateReplacesFirstReport(t *testing.T) {
	firstReportTime := fixedNow.Add(-9 * time.Hour)
	updateTime := fixedNow.Add(-1 * time.Hour)

	current := clustering.RepresentativeCandidate{
		ArticleID: "injury-1",
		Representation: eventsignals.Representation{
			ArticleID:     "injury-1",
			SourceID:      "source-a",
			Domain:        "gridirontalk.example.com",
			PrimaryEntity: "Jaylen Wickersham",
			EventType:     eventsignals.EventTypeInjury,
			Action:        eventsignals.ActionInjure,
			EventDate:     firstReportTime,
			PublishedAt:   firstReportTime,
		},
		Editorial: neutralEditorial,
	}
	update := clustering.RepresentativeCandidate{
		ArticleID: "injury-update-1",
		Representation: eventsignals.Representation{
			ArticleID:     "injury-update-1",
			SourceID:      "source-b",
			Domain:        "isotopes.example.com",
			PrimaryEntity: "Jaylen Wickersham",
			Opponent:      "Shelbyville Sharks",
			EventType:     eventsignals.EventTypeInjury,
			Action:        eventsignals.ActionReturn,
			EventDate:     updateTime,
			PublishedAt:   updateTime,
		},
		IsPrimarySource: true,
		Editorial:       neutralEditorial,
	}

	replaces, score := clustering.ReplacesCurrent(current, update, testRepresentativeConfig(), fixedNow)

	if !replaces {
		t.Fatalf("ReplacesCurrent = false, want true (return-to-practice update names a new opponent and progresses the action from injure to return, from a higher-priority primary source)")
	}
	if score.ArticleID != "injury-update-1" {
		t.Errorf("score.ArticleID = %q, want injury-update-1", score.ArticleID)
	}
}

func TestRankRepresentative_EvidenceIsDeterministic(t *testing.T) {
	published := fixedNow.Add(-2 * time.Hour)

	candidates := []clustering.RepresentativeCandidate{
		{
			ArticleID: "c1",
			Representation: eventsignals.Representation{
				ArticleID:     "c1",
				SourceID:      "source-a",
				Domain:        "isotopes.example.com",
				PrimaryEntity: "Ezo Quarrington",
				Opponent:      "Shelbyville Sharks",
				Competition:   "NFL",
				EventType:     eventsignals.EventTypeTransaction,
				Action:        eventsignals.ActionSign,
				EventDate:     published,
				PublishedAt:   published,
				Entities:      []string{"Ezo Quarrington", "Shelbyville Sharks"},
			},
			IsPrimarySource: true,
			Editorial:       neutralEditorial,
		},
		{
			ArticleID: "c2",
			Representation: eventsignals.Representation{
				ArticleID:     "c2",
				SourceID:      "source-b",
				Domain:        "gridirontalk.example.com",
				PrimaryEntity: "Ezo Quarrington",
				EventType:     eventsignals.EventTypeConfirmation,
				ContentSignal: eventsignals.ContentSignalConfirmation,
				Action:        eventsignals.ActionConfirm,
				EventDate:     published.Add(30 * time.Minute),
				PublishedAt:   published.Add(30 * time.Minute),
			},
			Editorial: neutralEditorial,
		},
		{
			ArticleID: "c3",
			Representation: eventsignals.Representation{
				ArticleID:     "c3",
				SourceID:      "source-c",
				Domain:        "randomsportsblog.example",
				PrimaryEntity: "Ezo Quarrington",
				EventType:     eventsignals.EventTypeRumor,
				ContentSignal: eventsignals.ContentSignalRumor,
				EventDate:     published.Add(-1 * time.Hour),
				PublishedAt:   published.Add(-1 * time.Hour),
			},
			ClickbaitPenalty: 0.4,
			Editorial:        neutralEditorial,
		},
	}
	cfg := testRepresentativeConfig()

	first := clustering.RankRepresentative(candidates, cfg, fixedNow)
	second := clustering.RankRepresentative(candidates, cfg, fixedNow)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("RankRepresentative is not deterministic across identical calls:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	for _, score := range first {
		if len(score.Evidence) == 0 {
			t.Errorf("ArticleID %q: Evidence is empty, want an explanation of every signal considered", score.ArticleID)
		}
	}
	if len(first) != 3 {
		t.Fatalf("len(first) = %d, want 3", len(first))
	}
}
