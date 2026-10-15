package clustereval_test

import (
	"math"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// testRules is a small, fully-controlled eventsignals.Rules fixture (a
// handful of entities/actions, no synonyms/content cues needed by these
// tests) so every test's expected eventsignals.Representation — and
// therefore every clustering.DecideJoin outcome — can be hand-computed,
// mirroring clustering_test.go's own testConfig()/baseRepresentation()
// fixtures rather than depending on the much larger production
// configs/event_clustering_rules.yml vocabulary.
func testRules() eventsignals.Rules {
	return eventsignals.Rules{
		ActionTerms: map[eventsignals.Action][]string{
			eventsignals.ActionSign:    {"sign", "signs", "signed"},
			eventsignals.ActionRelease: {"release", "released", "releases"},
			eventsignals.ActionInjure:  {"injured", "injury"},
		},
		Entities: []eventsignals.EntityRule{
			{CanonicalName: "Alpha", Kind: eventsignals.EntityKindPlayer, Terms: []string{"alpha"}},
			{CanonicalName: "Beta", Kind: eventsignals.EntityKindPlayer, Terms: []string{"beta"}},
		},
	}
}

// testConfig mirrors clustering_test.go's testConfig(): the same weights/
// thresholds, chosen so every scenario below (join/ambiguous/create) can be
// computed by hand from Compare's documented weighted-average formula.
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

func at(offset time.Duration) time.Time { return fixedNow.Add(offset) }

func art(id, title, sourceID string, publishedAt time.Time) clustereval.DatasetArticle {
	return clustereval.DatasetArticle{
		ID:          id,
		Title:       title,
		SourceID:    sourceID,
		Domain:      "example.com",
		ContentType: "article",
		PublishedAt: publishedAt,
	}
}

// -- pairwise / purity / over-grouped / fragmented -------------------------

func TestEvaluate_TwoCorrectlySeparatedGroups_ScoresPrecisionRecallF1PurityAtOne(t *testing.T) {
	dataset := clustereval.Dataset{Groups: []clustereval.Group{
		{ID: "g1", Articles: []clustereval.DatasetArticle{
			art("p1a", "Alpha Signs New Deal", "src-1", at(0)),
			art("p1b", "Alpha Signed Extension Reported By Second Outlet", "src-2", at(time.Hour)),
		}},
		{ID: "g2", Articles: []clustereval.DatasetArticle{
			art("p2a", "Beta Injured At Practice", "src-1", at(2*time.Hour)),
			art("p2b", "Beta Injury Update Reported", "src-2", at(3*time.Hour)),
		}},
	}}

	report, err := clustereval.Evaluate(dataset, testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	want := clustereval.PairwiseMetrics{TP: 2, FP: 0, FN: 0, TN: 4}
	if report.Pairwise != want {
		t.Fatalf("Pairwise = %+v, want %+v", report.Pairwise, want)
	}
	if !almostEqual(report.Pairwise.Precision(), 1.0) || !almostEqual(report.Pairwise.Recall(), 1.0) || !almostEqual(report.Pairwise.F1(), 1.0) {
		t.Errorf("Precision/Recall/F1 = %v/%v/%v, want all 1.0", report.Pairwise.Precision(), report.Pairwise.Recall(), report.Pairwise.F1())
	}
	if !almostEqual(report.Purity, 1.0) {
		t.Errorf("Purity = %v, want 1.0", report.Purity)
	}
	if report.OverGroupedClusters != 0 {
		t.Errorf("OverGroupedClusters = %d, want 0", report.OverGroupedClusters)
	}
	if report.FragmentedClusters != 0 {
		t.Errorf("FragmentedClusters = %d, want 0", report.FragmentedClusters)
	}
}

// mixedDataset builds a 4-article fixture with one over-grouped predicted
// cluster (two gold groups false-merged) and one fragmented gold group (its
// articles split across two predicted clusters):
//
//   - predicted cluster A: g1a (gold g1), g1b (gold g1), g2a (gold g2) —
//     g2a shares g1a/g1b's entity/action/event-type by construction, so
//     DecideJoin (correctly, given the signals available) joins it to A
//     even though the dataset says it is a different real-world event —
//     an over-grouped predicted cluster.
//   - predicted cluster B: g2b (gold g2) alone — g2b names a different
//     primary entity than everything already in A, which
//     clustering.Compare's conflict gate hard-disqualifies regardless of
//     any other shared signal, so it starts its own cluster — leaving gold
//     group g2's two articles fragmented across A and B.
func mixedDataset() clustereval.Dataset {
	return clustereval.Dataset{Groups: []clustereval.Group{
		{ID: "g1", Articles: []clustereval.DatasetArticle{
			art("g1a", "Alpha Signs New Deal", "src-1", at(0)),
			art("g1b", "Alpha Signed Extension Reported By Second Outlet", "src-2", at(time.Hour)),
		}},
		{ID: "g2", Articles: []clustereval.DatasetArticle{
			art("g2a", "Alpha Signs Extension Too", "src-3", at(2*time.Hour)),
			art("g2b", "Beta Injured At Practice", "src-1", at(3*time.Hour)),
		}},
	}}
}

func TestEvaluate_OverGroupedAndFragmented_PairwiseMetricsReflectBothErrors(t *testing.T) {
	report, err := clustereval.Evaluate(mixedDataset(), testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	want := clustereval.PairwiseMetrics{TP: 1, FP: 2, FN: 1, TN: 2}
	if report.Pairwise != want {
		t.Fatalf("Pairwise = %+v, want %+v", report.Pairwise, want)
	}
	if !almostEqual(report.Pairwise.Precision(), 1.0/3.0) {
		t.Errorf("Precision = %v, want 1/3", report.Pairwise.Precision())
	}
	if !almostEqual(report.Pairwise.Recall(), 0.5) {
		t.Errorf("Recall = %v, want 0.5", report.Pairwise.Recall())
	}
	if !almostEqual(report.Pairwise.F1(), 0.4) {
		t.Errorf("F1 = %v, want 0.4", report.Pairwise.F1())
	}
}

func TestEvaluate_OverGroupedAndFragmented_ComputesPurity(t *testing.T) {
	report, err := clustereval.Evaluate(mixedDataset(), testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if !almostEqual(report.Purity, 0.75) {
		t.Errorf("Purity = %v, want 0.75 (majority counts 2+1 over 4 articles)", report.Purity)
	}
}

func TestEvaluate_OverGroupedAndFragmented_CountsOverGroupedCluster(t *testing.T) {
	report, err := clustereval.Evaluate(mixedDataset(), testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if report.OverGroupedClusters != 1 {
		t.Errorf("OverGroupedClusters = %d, want 1 (predicted cluster A mixes gold g1 and g2)", report.OverGroupedClusters)
	}
}

func TestEvaluate_OverGroupedAndFragmented_CountsFragmentedGoldGroup(t *testing.T) {
	report, err := clustereval.Evaluate(mixedDataset(), testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if report.FragmentedClusters != 1 {
		t.Errorf("FragmentedClusters = %d, want 1 (gold group g2 split across predicted clusters A and B)", report.FragmentedClusters)
	}
}

// -- ambiguous handling ------------------------------------------------

// ambiguousPair returns two DatasetArticles whose Representations score
// squarely inside testConfig()'s ambiguous band ([AmbiguousHigh, 0.70)):
// same entity (Alpha) and same event type (transaction, since sign/release
// both map to EventTypeTransaction), but a mismatched action and the same
// SourceID (so the SourcePriorityWeight signal doesn't push the score back
// up). Hand-computed score: (entity .30 + eventType .20 + novelty .05) /
// (entity .30 + action .20 + eventType .20 + source .05 + novelty .05) =
// 0.55/0.80 = 0.6875, which falls in [AmbiguousHigh=0.55, JoinThreshold=
// 0.70) => ActionAmbiguous.
func ambiguousPair() (first, second clustereval.DatasetArticle) {
	first = art("amb-first", "Alpha Signs New Deal", "src-1", at(0))
	second = art("amb-second", "Alpha Released By Team", "src-1", at(time.Hour))
	return first, second
}

func TestEvaluate_ExpectedAmbiguousArticle_ExcludedFromPairwiseButReportedSeparately(t *testing.T) {
	first, second := ambiguousPair()
	second.ExpectedAmbiguous = true

	dataset := clustereval.Dataset{Groups: []clustereval.Group{
		{ID: "gA", Articles: []clustereval.DatasetArticle{first, second}},
		{ID: "gZ", Articles: []clustereval.DatasetArticle{art("z", "Beta Injured At Practice", "src-1", at(2*time.Hour))}},
	}}

	report, err := clustereval.Evaluate(dataset, testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	total := report.Pairwise.TP + report.Pairwise.FP + report.Pairwise.FN + report.Pairwise.TN
	if total != 1 {
		t.Errorf("pairwise total = %d, want 1 (only the 1 pair among the 2 non-excluded articles)", total)
	}
	if report.AmbiguousExpected != 1 {
		t.Errorf("AmbiguousExpected = %d, want 1", report.AmbiguousExpected)
	}
	if report.AmbiguousCorrectlyFlagged != 1 {
		t.Errorf("AmbiguousCorrectlyFlagged = %d, want 1 (DecideJoin does return ActionAmbiguous for it)", report.AmbiguousCorrectlyFlagged)
	}
}

func TestEvaluate_UnexpectedAmbiguousArticle_CountedAndStillScoredInPairwise(t *testing.T) {
	first, second := ambiguousPair()
	// second is NOT marked ExpectedAmbiguous, but the dataset still claims
	// it belongs to the same gold event as first — DecideJoin will return
	// ActionAmbiguous for it regardless (see ambiguousPair's doc comment),
	// which is itself a prediction error this test expects to see reflected
	// in the pairwise metrics rather than silently excluded.
	dataset := clustereval.Dataset{Groups: []clustereval.Group{
		{ID: "gA", Articles: []clustereval.DatasetArticle{first, second}},
	}}

	report, err := clustereval.Evaluate(dataset, testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if report.UnexpectedAmbiguous != 1 {
		t.Errorf("UnexpectedAmbiguous = %d, want 1", report.UnexpectedAmbiguous)
	}
	if report.AmbiguousExpected != 0 {
		t.Errorf("AmbiguousExpected = %d, want 0", report.AmbiguousExpected)
	}
	want := clustereval.PairwiseMetrics{TP: 0, FP: 0, FN: 1, TN: 0}
	if report.Pairwise != want {
		t.Errorf("Pairwise = %+v, want %+v (same gold group, different predicted cluster because ActionAmbiguous still creates a new cluster)", report.Pairwise, want)
	}
}

// -- representative accuracy --------------------------------------------

func TestEvaluate_RepresentativeAccuracy_CorrectAndIncorrectPicks(t *testing.T) {
	repCfg := clustering.RepresentativeConfig{PrimarySourceWeight: 1.0}

	dataset := clustereval.Dataset{Groups: []clustereval.Group{
		{
			ID:             "rg1",
			Representative: "r1",
			Articles: []clustereval.DatasetArticle{
				{ID: "r1", Title: "Alpha Signs New Deal", SourceID: "src-official", ContentType: "article", PublishedAt: at(0), IsPrimarySource: true},
				{ID: "r2", Title: "Alpha Signed Extension Reported By Second Outlet", SourceID: "src-aggregator", ContentType: "article", PublishedAt: at(time.Hour), IsPrimarySource: false},
			},
		},
		{
			// Declares the non-primary-source article as representative,
			// even though RankRepresentative (PrimarySourceWeight-only
			// config) will always rank the primary-source article s2 on
			// top — an intentionally incorrect gold pick.
			ID:             "rg2",
			Representative: "s1",
			Articles: []clustereval.DatasetArticle{
				{ID: "s1", Title: "Alpha Signs New Deal", SourceID: "src-1", ContentType: "article", PublishedAt: at(2 * time.Hour), IsPrimarySource: false},
				{ID: "s2", Title: "Alpha Signed Extension Reported By Second Outlet", SourceID: "src-2", ContentType: "article", PublishedAt: at(3 * time.Hour), IsPrimarySource: true},
			},
		},
	}}

	report, err := clustereval.Evaluate(dataset, testRules(), testConfig(), repCfg, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if report.RepresentativeTotal != 2 {
		t.Fatalf("RepresentativeTotal = %d, want 2", report.RepresentativeTotal)
	}
	if report.RepresentativeCorrect != 1 {
		t.Errorf("RepresentativeCorrect = %d, want 1", report.RepresentativeCorrect)
	}
	if !almostEqual(report.RepresentativeAccuracy, 0.5) {
		t.Errorf("RepresentativeAccuracy = %v, want 0.5", report.RepresentativeAccuracy)
	}
}

// -- member role tracking ------------------------------------------------

func TestEvaluate_MemberRoleTracking_ComparesExpectedToActual(t *testing.T) {
	a := art("p1a", "Alpha Signs New Deal", "src-1", at(0))
	a.ExpectedRole = "first_report"

	b := art("p1b", "Alpha Signed Extension Reported By Second Outlet", "src-2", at(time.Hour))
	b.ExpectedRole = "confirmation"

	// c joins the same cluster via action "release" (differs from a/b's
	// "sign") — clustering.Compare classifies this RelationUpdate and
	// selectMemberRole assigns MemberRoleSubstantialUpdate, not the
	// "correction" this dataset entry (deliberately, for this test)
	// expects, exercising the mismatch-counts-as-wrong path.
	c := art("p1c", "Alpha Released By Team", "src-3", at(2*time.Hour))
	c.ExpectedRole = "correction"

	dataset := clustereval.Dataset{Groups: []clustereval.Group{
		{ID: "g1", Articles: []clustereval.DatasetArticle{a, b, c}},
	}}

	report, err := clustereval.Evaluate(dataset, testRules(), testConfig(), clustering.RepresentativeConfig{}, fixedNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if report.RoleTotal != 3 {
		t.Fatalf("RoleTotal = %d, want 3", report.RoleTotal)
	}
	if report.RoleCorrect != 2 {
		t.Errorf("RoleCorrect = %d, want 2 (first_report and confirmation correct, correction wrong: actual is substantial_update)", report.RoleCorrect)
	}
}

// -- PairwiseMetrics ------------------------------------------------------

func TestPairwiseMetrics_ZeroDenominatorsAreZeroNotNaN(t *testing.T) {
	var m clustereval.PairwiseMetrics
	if m.Precision() != 0 || m.Recall() != 0 || m.F1() != 0 {
		t.Errorf("zero-denominator metrics = %v/%v/%v, want all 0", m.Precision(), m.Recall(), m.F1())
	}
}

// -- LoadDataset -----------------------------------------------------------

func TestLoadDataset_ParsesYAMLFile(t *testing.T) {
	dataset, err := clustereval.LoadDataset(repofs.Path(t, "testdata", "clustering_groups.yml"))
	if err != nil {
		t.Fatalf("LoadDataset() error = %v", err)
	}
	if len(dataset.Groups) == 0 {
		t.Fatal("LoadDataset() returned no groups")
	}
	for i, g := range dataset.Groups {
		if g.ID == "" {
			t.Errorf("group %d has an empty ID", i)
		}
		if len(g.Articles) == 0 {
			t.Errorf("group %q has no articles", g.ID)
		}
		for _, a := range g.Articles {
			if a.ID == "" {
				t.Errorf("group %q has an article with an empty ID", g.ID)
			}
			if a.Title == "" {
				t.Errorf("group %q article %q has an empty title", g.ID, a.ID)
			}
			if a.PublishedAt.IsZero() {
				t.Errorf("group %q article %q has a zero published_at", g.ID, a.ID)
			}
		}
	}
}

func TestLatestPublishedAt(t *testing.T) {
	if got := clustereval.LatestPublishedAt(clustereval.Dataset{}); !got.IsZero() {
		t.Fatalf("empty dataset = %v, want the zero time", got)
	}
	early := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(48 * time.Hour)
	ds := clustereval.Dataset{Groups: []clustereval.Group{
		{Articles: []clustereval.DatasetArticle{{PublishedAt: early}}},
		{Articles: []clustereval.DatasetArticle{{PublishedAt: late}, {PublishedAt: early}}},
	}}
	if got := clustereval.LatestPublishedAt(ds); !got.Equal(late) {
		t.Fatalf("LatestPublishedAt() = %v, want %v", got, late)
	}
}

func TestCountChangedSignals_CountsArticlesWhoseTypeActionOrSignalDiffers(t *testing.T) {
	dataset := clustereval.Dataset{Groups: []clustereval.Group{{ID: "g1", Articles: []clustereval.DatasetArticle{
		{ID: "a1", Title: "Alpha inks a deal", SourceID: "src", PublishedAt: fixedNow},
		{ID: "a2", Title: "Beta signed", SourceID: "src", PublishedAt: fixedNow},
		{ID: "a3", Title: "Rumor mill on Beta", SourceID: "src", PublishedAt: fixedNow},
	}}}}
	before := testRules()
	if n, err := clustereval.CountChangedSignals(dataset, before, before, fixedNow); err != nil || n != 0 {
		t.Fatalf("identical rules: n=%d err=%v, want 0", n, err)
	}
	after := testRules()
	after.ActionTerms = map[eventsignals.Action][]string{
		eventsignals.ActionSign:    {"sign", "signs", "signed", "inks a deal"},
		eventsignals.ActionRelease: before.ActionTerms[eventsignals.ActionRelease],
		eventsignals.ActionInjure:  before.ActionTerms[eventsignals.ActionInjure],
	}
	after.ContentCues = map[eventsignals.ContentSignal][]string{eventsignals.ContentSignalRumor: {"rumor mill"}}
	n, err := clustereval.CountChangedSignals(dataset, before, after, fixedNow)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2 (a1 gains an action, a3 a signal; a2 unchanged)", n, err)
	}
}
