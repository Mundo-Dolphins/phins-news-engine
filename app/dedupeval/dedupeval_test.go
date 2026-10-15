package dedupeval_test

import (
	"math"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dedupeval"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func defaultTextConfig() normalization.TextConfig {
	return normalization.TextConfig{ShingleSize: 3, MinTokenLength: 3}
}

func defaultThresholdConfig() deduplication.ThresholdConfig {
	return deduplication.ThresholdConfig{
		DefaultThreshold: 0.82, ShortTextMaxTokens: 8, ShortTextThreshold: 0.90, EventThreshold: 0.55, RulesVersion: "dedup-rules-v1",
	}
}

func TestEvaluate_PerfectDatasetScoresPrecisionRecallF1OfOne(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	dataset := dedupeval.Dataset{Pairs: []dedupeval.Pair{
		{
			A:     dedupeval.PairArticle{Title: "Isotopes Beat Sharks In Overtime Thriller", SourceType: "rss", ContentType: "article", PublishedAt: now},
			B:     dedupeval.PairArticle{Title: "Isotopes Beat Sharks In An Overtime Thriller", SourceType: "rss", ContentType: "article", PublishedAt: now},
			Label: "same_content",
		},
		{
			A:     dedupeval.PairArticle{Title: "Isotopes Sign Free Agent Cornerback", SourceType: "rss", ContentType: "article", PublishedAt: now},
			B:     dedupeval.PairArticle{Title: "Weather Forecast Calls For Rain This Weekend", SourceType: "rss", ContentType: "article", PublishedAt: now},
			Label: "different",
		},
	}}

	report := dedupeval.Evaluate(dataset, defaultTextConfig(), defaultThresholdConfig(), "jaccard")
	if !almostEqual(report.Overall.Precision(), 1.0) || !almostEqual(report.Overall.Recall(), 1.0) || !almostEqual(report.Overall.F1(), 1.0) {
		t.Errorf("Overall = %+v, want precision=recall=f1=1.0", report.Overall)
	}
	if report.Excluded != 0 {
		t.Errorf("Excluded = %d, want 0", report.Excluded)
	}
}

func TestEvaluate_ExcludesAmbiguousPairsFromMetrics(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	dataset := dedupeval.Dataset{Pairs: []dedupeval.Pair{
		{
			A:     dedupeval.PairArticle{Title: "Isotopes Injury Report Update", SourceType: "rss", ContentType: "article", PublishedAt: now},
			B:     dedupeval.PairArticle{Title: "Isotopes Release Injury Report", SourceType: "rss", ContentType: "article", PublishedAt: now},
			Label: "ambiguous",
		},
	}}

	report := dedupeval.Evaluate(dataset, defaultTextConfig(), defaultThresholdConfig(), "jaccard")
	if report.Excluded != 1 {
		t.Errorf("Excluded = %d, want 1", report.Excluded)
	}
	total := report.Overall.TP + report.Overall.FP + report.Overall.FN + report.Overall.TN
	if total != 0 {
		t.Errorf("Overall counts sum to %d, want 0 (ambiguous excluded)", total)
	}
}

func TestEvaluate_ConfusionMatrixDistinguishesSameContentFromSameEvent(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	dataset := dedupeval.Dataset{Pairs: []dedupeval.Pair{
		{
			A:     dedupeval.PairArticle{Title: "Isotopes Sign Veteran Linebacker To Bolster Defense", SourceType: "rss", ContentType: "article", PublishedAt: now},
			B:     dedupeval.PairArticle{Title: "Report: Isotopes Have Signed A Veteran Linebacker", SourceType: "rss", ContentType: "article", PublishedAt: now},
			Label: "same_event",
		},
	}}

	report := dedupeval.Evaluate(dataset, defaultTextConfig(), defaultThresholdConfig(), "jaccard")
	found := false
	for _, row := range report.Confusion {
		if row["same_event"] > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("Confusion = %+v, want at least one same_event gold-label entry", report.Confusion)
	}
}

func TestEvaluate_BreaksDownBySourceTypeAndContentType(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	dataset := dedupeval.Dataset{Pairs: []dedupeval.Pair{
		{
			A:     dedupeval.PairArticle{Title: "Isotopes Sign Free Agent", SourceType: "twitter", ContentType: "social_post", PublishedAt: now},
			B:     dedupeval.PairArticle{Title: "Different Unrelated Headline Entirely", SourceType: "twitter", ContentType: "social_post", PublishedAt: now},
			Label: "different",
		},
	}}

	report := dedupeval.Evaluate(dataset, defaultTextConfig(), defaultThresholdConfig(), "jaccard")
	if _, ok := report.BySourceType["twitter"]; !ok {
		t.Error("BySourceType missing twitter breakdown")
	}
	if _, ok := report.ByContentType["social_post"]; !ok {
		t.Error("ByContentType missing social_post breakdown")
	}
}

func TestLoadDataset_ParsesYAMLFile(t *testing.T) {
	dataset, err := dedupeval.LoadDataset(repofs.Path(t, "testdata", "dedup_pairs.yml"))
	if err != nil {
		t.Fatalf("LoadDataset() error = %v", err)
	}
	if len(dataset.Pairs) == 0 {
		t.Fatal("LoadDataset() returned no pairs")
	}
	for i, p := range dataset.Pairs {
		if p.Label == "" {
			t.Errorf("pair %d has an empty label", i)
		}
	}
}

func TestBinaryMetrics_PrecisionRecallF1ZeroDenominatorsAreZeroNotNaN(t *testing.T) {
	var m dedupeval.BinaryMetrics
	if m.Precision() != 0 || m.Recall() != 0 || m.F1() != 0 {
		t.Errorf("zero-denominator metrics = %v/%v/%v, want all 0", m.Precision(), m.Recall(), m.F1())
	}
}

func TestEvaluate_DiceMethodIsActuallyUsedNotJaccard(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	dataset := dedupeval.Dataset{Pairs: []dedupeval.Pair{
		{
			A:     dedupeval.PairArticle{Title: "Isotopes Sign Speedy Rookie Wide Receiver Today", SourceType: "rss", ContentType: "article", PublishedAt: now},
			B:     dedupeval.PairArticle{Title: "Isotopes Sign Speedy Rookie Wide Cornerback Instead", SourceType: "rss", ContentType: "article", PublishedAt: now},
			Label: "same_event",
		},
	}}

	// 5-token titles share 3 of 5 shingles: Jaccard = 3/7 ≈ 0.43 (band
	// "none", below the 0.55 event threshold) but Dice = 2*3/10 = 0.6 (band
	// "same_event"). A method-dispatch bug that silently fell back to
	// Jaccard for "dice" would record a false negative here instead of a
	// true positive.
	report := dedupeval.Evaluate(dataset, defaultTextConfig(), defaultThresholdConfig(), "dice")

	want := dedupeval.BinaryMetrics{TP: 1, FP: 0, FN: 0, TN: 0}
	if report.Overall != want {
		t.Errorf("Overall = %+v, want %+v", report.Overall, want)
	}
	if report.Confusion["same_event"]["same_event"] != 1 {
		t.Errorf("Confusion[same_event][same_event] = %d, want 1", report.Confusion["same_event"]["same_event"])
	}
}

func TestEvaluate_RecordsFalsePositiveWhenPredictedRelatedButGoldDifferent(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	dataset := dedupeval.Dataset{Pairs: []dedupeval.Pair{
		{
			A:     dedupeval.PairArticle{Title: "Isotopes Beat Sharks In Overtime Thriller", SourceType: "rss", ContentType: "article", PublishedAt: now},
			B:     dedupeval.PairArticle{Title: "Isotopes Beat Sharks In An Overtime Thriller", SourceType: "rss", ContentType: "article", PublishedAt: now},
			Label: "different",
		},
	}}

	// Same near-identical pair as TestEvaluate_PerfectDatasetScoresPrecisionRecallF1OfOne
	// (Jaccard 1.0, predicted near_duplicate) but mislabeled "different", so
	// the true prediction is wrong: an FP, not a TN. A branch-order bug in
	// BinaryMetrics.record swapping FP and TN would pass every other test
	// but fail this one.
	report := dedupeval.Evaluate(dataset, defaultTextConfig(), defaultThresholdConfig(), "jaccard")

	want := dedupeval.BinaryMetrics{TP: 0, FP: 1, FN: 0, TN: 0}
	if report.Overall != want {
		t.Errorf("Overall = %+v, want %+v", report.Overall, want)
	}
}
