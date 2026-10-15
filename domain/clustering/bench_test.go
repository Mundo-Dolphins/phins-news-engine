package clustering_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// benchActions/benchEventTypes/benchSources supply varied values for the
// fixture builders below, so the 50/20-element slices built from them are
// not identical copies of one another. All values come from the real
// eventsignals constants used throughout clustering_test.go and
// representative_test.go (see baseRepresentation in clustering_test.go and
// the literal Representation values in representative_test.go).
var (
	benchActions = []eventsignals.Action{
		eventsignals.ActionSign, eventsignals.ActionExtend, eventsignals.ActionInjure,
		eventsignals.ActionReturn, eventsignals.ActionConfirm, eventsignals.ActionReport,
	}
	benchEventTypes = []eventsignals.EventType{
		eventsignals.EventTypeTransaction, eventsignals.EventTypeInjury,
		eventsignals.EventTypeRumor, eventsignals.EventTypeConfirmation,
		eventsignals.EventTypeQuote,
	}
	benchSources = []string{"source-a", "source-b", "source-c", "source-d", "source-e"}
	benchDomains = []string{
		"isotopes.example.com", "gridirontalk.example.com", "sportsnet.example.com",
		"randomsportsblog.example", "gridironrumors.example.com",
	}
)

// BenchmarkDecideJoin_50Candidates mirrors the configured CandidateLimit
// (defaultClusterCandidateLimit = 50, see engine.Run and
// app/usecase/process_cycle.go) — the realistic upper
// bound for one clustering decision. Candidates are spread across 10
// clusters and vary action/event-type/source/time-offset so that some
// trigger Compare's conflict gate (score 0, cheap — an incompatible event
// type/action pairing or an out-of-window date, both structurally possible
// in production: the same entity can have several unrelated stories) and
// others reach the full weighted-average scoring path, matching a
// realistic mixed candidate set rather than a uniform best/worst case.
//
// PrimaryEntity is deliberately held constant, matching incoming: in
// production DecideJoin only ever sees candidates FindCandidatesByEventKeys
// already filtered to the same PrimaryEntity (eventsignals.
// buildApproximateKeys' weakest generated key is PrimaryEntity alone, and
// every stronger key also includes it — see eventsignals.go's
// buildApproximateKeys), so an entity-mismatched candidate can never
// actually reach DecideJoin. Varying PrimaryEntity here would trip
// Compare's conflict gate immediately (see detectConflict) on a path the
// real call site structurally never exercises, understating DecideJoin's
// true per-call cost at the configured 50-candidate ceiling.
func BenchmarkDecideJoin_50Candidates(b *testing.B) {
	const incomingEntity = "Ezo Quarrington"
	incoming := baseRepresentation("incoming", "source-a", incomingEntity, eventsignals.EventTypeTransaction, eventsignals.ActionExtend, fixedNow)

	const n = 50
	candidates := make([]clustering.Candidate, n)
	for i := range candidates {
		action := benchActions[i%len(benchActions)]
		eventType := benchEventTypes[i%len(benchEventTypes)]
		source := benchSources[i%len(benchSources)]
		clusterID := fmt.Sprintf("cluster-%d", i%10)
		// Offsets span -75h..+72h from fixedNow, crossing the 48h
		// EventWindow baseRepresentation sets: roughly a third of
		// candidates (|i-25| > 16) land outside the window and are
		// disqualified by the date-window conflict gate; the rest fall
		// inside it and, combined with the action/event-type cycling
		// above, produce a realistic mix of full-conflict and
		// full-weighted-average-scoring candidates.
		offset := time.Duration(i-25) * 3 * time.Hour

		candidates[i] = clustering.Candidate{
			ArticleID:      fmt.Sprintf("existing-%d", i),
			ClusterID:      clusterID,
			Representation: baseRepresentation(fmt.Sprintf("existing-%d", i), source, incomingEntity, eventType, action, fixedNow.Add(offset)),
		}
	}

	cfg := testConfig()
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clustering.DecideJoin(incoming, candidates, cfg, now)
	}
}

// benchRepresentativeCandidate builds one varied RepresentativeCandidate for
// the RankRepresentative/ReplacesCurrent benchmarks below, following the
// literal field patterns used throughout representative_test.go (e.g.
// TestRankRepresentative_EvidenceIsDeterministic).
func benchRepresentativeCandidate(i int, articleID string, published time.Time) clustering.RepresentativeCandidate {
	domain := benchDomains[i%len(benchDomains)]
	source := benchSources[i%len(benchSources)]
	action := benchActions[i%len(benchActions)]
	eventType := benchEventTypes[i%len(benchEventTypes)]

	rep := eventsignals.Representation{
		ArticleID:     articleID,
		SourceID:      source,
		Domain:        domain,
		PrimaryEntity: "Ezo Quarrington", // same event/cluster: shared entity across members
		EventType:     eventType,
		Action:        action,
		EventDate:     published,
		PublishedAt:   published,
	}
	if i%4 == 0 {
		rep.Opponent = "Shelbyville Sharks"
	}
	if i%5 == 0 {
		rep.Competition = "NFL"
	}
	if i%3 == 0 {
		rep.Entities = []string{"Ezo Quarrington", "Shelbyville Sharks"}
	}
	if eventType == eventsignals.EventTypeRumor {
		rep.ContentSignal = eventsignals.ContentSignalRumor
	}
	if eventType == eventsignals.EventTypeConfirmation {
		rep.ContentSignal = eventsignals.ContentSignalConfirmation
	}

	return clustering.RepresentativeCandidate{
		ArticleID:        articleID,
		Representation:   rep,
		IsPrimarySource:  i%6 == 0,
		ClickbaitPenalty: float64(i%5) / 10, // 0.0, 0.1, 0.2, 0.3, 0.4
		Editorial:        editorial.Score{Value: editorial.NeutralScore, Placeholder: true},
	}
}

// BenchmarkRankRepresentative_20Members exercises the whole-cluster
// re-ranking path (as opposed to the incremental ReplacesCurrent path
// ProcessCycle actually calls per accepted join) at a realistic large
// cluster size, with members varying source/domain/action/event-type/
// publish-time/clickbait-penalty so scoring isn't a uniform best case.
func BenchmarkRankRepresentative_20Members(b *testing.B) {
	const n = 20
	base := fixedNow.Add(-20 * time.Hour)
	candidates := make([]clustering.RepresentativeCandidate, n)
	for i := range candidates {
		published := base.Add(time.Duration(i) * time.Hour)
		candidates[i] = benchRepresentativeCandidate(i, fmt.Sprintf("member-%d", i), published)
	}

	cfg := testRepresentativeConfig()
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clustering.RankRepresentative(candidates, cfg, now)
	}
}

// BenchmarkReplacesCurrent exercises the incremental one-vs-current
// comparison ProcessCycle actually calls per accepted join, with current
// and candidate deliberately built from different fixture indices so they
// are not identical (current is the earlier, weaker report; candidate is a
// later, higher-priority substantial update — mirroring
// TestReplacesCurrent_SubstantialUpdateExceedsSwitchMargin_Replaces).
func BenchmarkReplacesCurrent(b *testing.B) {
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
		Editorial: editorial.Score{Value: editorial.NeutralScore, Placeholder: true},
	}
	candidate := clustering.RepresentativeCandidate{
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
		Editorial:       editorial.Score{Value: editorial.NeutralScore, Placeholder: true},
	}

	cfg := testRepresentativeConfig()
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clustering.ReplacesCurrent(current, candidate, cfg, now)
	}
}
