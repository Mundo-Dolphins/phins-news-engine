package deduplication_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
)

func defaultThresholdConfig() deduplication.ThresholdConfig {
	return deduplication.ThresholdConfig{
		DefaultThreshold:      0.82,
		ThresholdBySourceType: map[string]float64{"twitter": 0.75, "rss": 0.85},
		ShortTextMaxTokens:    8,
		ShortTextThreshold:    0.90,
		EventThreshold:        0.55,
		RulesVersion:          "dedup-rules-v1",
	}
}

func TestResolveThresholds_UsesSourceTypeOverrideWhenPresent(t *testing.T) {
	got := deduplication.ResolveThresholds(defaultThresholdConfig(), "twitter", 20)
	if got.DuplicateThreshold != 0.75 {
		t.Errorf("DuplicateThreshold = %v, want twitter override 0.75", got.DuplicateThreshold)
	}
}

func TestResolveThresholds_FallsBackToDefaultForUnknownSourceType(t *testing.T) {
	got := deduplication.ResolveThresholds(defaultThresholdConfig(), "unknown", 20)
	if got.DuplicateThreshold != 0.82 {
		t.Errorf("DuplicateThreshold = %v, want default 0.82", got.DuplicateThreshold)
	}
}

func TestResolveThresholds_ShortTextOverridesSourceType(t *testing.T) {
	got := deduplication.ResolveThresholds(defaultThresholdConfig(), "twitter", 5)
	if got.DuplicateThreshold != 0.90 {
		t.Errorf("DuplicateThreshold = %v, want short-text override 0.90", got.DuplicateThreshold)
	}
}

func TestResolveThresholds_CarriesEventThresholdAndRulesVersion(t *testing.T) {
	got := deduplication.ResolveThresholds(defaultThresholdConfig(), "rss", 20)
	if got.EventThreshold != 0.55 || got.RulesVersion != "dedup-rules-v1" {
		t.Errorf("got = %+v, want EventThreshold=0.55 RulesVersion=dedup-rules-v1", got)
	}
}

func TestDecide_ScoreAboveDuplicateThresholdIsNear(t *testing.T) {
	candidates := []deduplication.Candidate{{ArticleID: "a1", ClusterID: "c1"}}
	scores := map[string]float64{"a1": 0.90}
	signals := map[string][]string{"a1": {"jaccard=0.90", "shared_terms=6"}}
	thresholds := deduplication.Thresholds{DuplicateThreshold: 0.82, EventThreshold: 0.55, RulesVersion: "dedup-rules-v1"}

	got := deduplication.Decide(candidates, scores, signals, thresholds)
	if !got.IsDuplicate || got.Type != deduplication.DuplicateTypeNear {
		t.Errorf("got = %+v, want IsDuplicate=true Type=near", got)
	}
	if got.MatchedArticleID != "a1" || got.MatchedClusterID != "c1" {
		t.Errorf("got matched = %s/%s, want a1/c1", got.MatchedArticleID, got.MatchedClusterID)
	}
	if got.Explanation == "" {
		t.Error("Explanation should be non-empty and human-readable")
	}
}

func TestDecide_ScoreInEventBandIsSameEventNotDuplicate(t *testing.T) {
	candidates := []deduplication.Candidate{{ArticleID: "a1", ClusterID: "c1"}}
	scores := map[string]float64{"a1": 0.60}
	thresholds := deduplication.Thresholds{DuplicateThreshold: 0.82, EventThreshold: 0.55, RulesVersion: "dedup-rules-v1"}

	got := deduplication.Decide(candidates, scores, map[string][]string{}, thresholds)
	if got.IsDuplicate {
		t.Error("IsDuplicate = true, want false for a same-event match")
	}
	if got.Type != deduplication.DuplicateTypeSameEvent {
		t.Errorf("Type = %v, want same_event", got.Type)
	}
	if got.MatchedClusterID != "c1" {
		t.Errorf("MatchedClusterID = %q, want c1 (clustering reuses this match)", got.MatchedClusterID)
	}
}

func TestDecide_ScoreBelowEventThresholdIsNone(t *testing.T) {
	candidates := []deduplication.Candidate{{ArticleID: "a1", ClusterID: "c1"}}
	scores := map[string]float64{"a1": 0.10}
	thresholds := deduplication.Thresholds{DuplicateThreshold: 0.82, EventThreshold: 0.55, RulesVersion: "dedup-rules-v1"}

	got := deduplication.Decide(candidates, scores, map[string][]string{}, thresholds)
	if got.IsDuplicate || got.Type != deduplication.DuplicateTypeNone {
		t.Errorf("got = %+v, want Type=none", got)
	}
}

func TestDecide_NoCandidatesIsNone(t *testing.T) {
	thresholds := deduplication.Thresholds{DuplicateThreshold: 0.82, EventThreshold: 0.55}
	got := deduplication.Decide(nil, map[string]float64{}, map[string][]string{}, thresholds)
	if got.Type != deduplication.DuplicateTypeNone {
		t.Errorf("Type = %v, want none", got.Type)
	}
}

func TestDecide_PicksHighestScoringCandidate(t *testing.T) {
	candidates := []deduplication.Candidate{
		{ArticleID: "low", ClusterID: "c-low"},
		{ArticleID: "high", ClusterID: "c-high"},
	}
	scores := map[string]float64{"low": 0.60, "high": 0.95}
	thresholds := deduplication.Thresholds{DuplicateThreshold: 0.82, EventThreshold: 0.55}

	got := deduplication.Decide(candidates, scores, map[string][]string{}, thresholds)
	if got.MatchedArticleID != "high" {
		t.Errorf("MatchedArticleID = %q, want high (highest score wins)", got.MatchedArticleID)
	}
}
