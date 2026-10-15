package editorialtrain_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func TestCapPerCluster_PrefersGoldOverEarlierSilver(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "c1", Tier: "silver", Domain: "d1.com", PublishedAt: t0},
		{ArticleID: "a2", ClusterID: "c1", Tier: "gold", Domain: "d2.com", PublishedAt: t0.Add(time.Hour)},
		{ArticleID: "a3", ClusterID: "c1", Tier: "silver", Domain: "d3.com", PublishedAt: t0.Add(2 * time.Hour)},
	}

	got := editorialtrain.CapPerCluster(examples, 1)
	if len(got) != 1 || got[0].ArticleID != "a2" {
		t.Errorf("CapPerCluster(cap=1) = %+v, want only a2 (gold beats earlier silver)", got)
	}
}

func TestCapPerCluster_EarliestBreaksSameTierTie(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "c1", Tier: "silver", Domain: "d1.com", PublishedAt: t0.Add(time.Hour)},
		{ArticleID: "a2", ClusterID: "c1", Tier: "silver", Domain: "d2.com", PublishedAt: t0},
	}

	got := editorialtrain.CapPerCluster(examples, 1)
	if len(got) != 1 || got[0].ArticleID != "a2" {
		t.Errorf("CapPerCluster(cap=1) = %+v, want only a2 (earliest of same tier)", got)
	}
}

func TestCapPerCluster_PrefersDistinctDomainsUnderHigherCap(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "c1", Tier: "gold", Domain: "same.com", PublishedAt: t0},
		{ArticleID: "a2", ClusterID: "c1", Tier: "gold", Domain: "same.com", PublishedAt: t0.Add(time.Hour)},
		{ArticleID: "a3", ClusterID: "c1", Tier: "gold", Domain: "other.com", PublishedAt: t0.Add(2 * time.Hour)},
	}

	got := editorialtrain.CapPerCluster(examples, 2)
	gotIDs := map[string]bool{}
	for _, e := range got {
		gotIDs[e.ArticleID] = true
	}
	if len(got) != 2 || !gotIDs["a1"] || !gotIDs["a3"] {
		t.Errorf("CapPerCluster(cap=2) = %+v, want a1 (earliest) + a3 (distinct domain over repeat a2)", got)
	}
}

func TestCapPerCluster_FillsCapWithRepeatDomainWhenNoneLeftDistinct(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "c1", Tier: "gold", Domain: "same.com", PublishedAt: t0},
		{ArticleID: "a2", ClusterID: "c1", Tier: "gold", Domain: "same.com", PublishedAt: t0.Add(time.Hour)},
	}

	got := editorialtrain.CapPerCluster(examples, 2)
	if len(got) != 2 {
		t.Errorf("CapPerCluster(cap=2) = %+v, want both kept (cap not smaller than the group, no diverse domain available)", got)
	}
}

func TestCapPerCluster_NeverCapsEmptyClusterID(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "", Domain: "d.com"},
		{ArticleID: "a2", ClusterID: "", Domain: "d.com"},
	}
	got := editorialtrain.CapPerCluster(examples, 1)
	if len(got) != 2 {
		t.Errorf("CapPerCluster() = %+v, want both empty-ClusterID examples untouched", got)
	}
}

func TestCapPerCluster_NonPositiveMaxDisablesCapping(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "c1"},
		{ArticleID: "a2", ClusterID: "c1"},
		{ArticleID: "a3", ClusterID: "c1"},
	}
	got := editorialtrain.CapPerCluster(examples, 0)
	if len(got) != 3 {
		t.Errorf("CapPerCluster(max=0) = %+v, want no capping", got)
	}
}

func TestCapPerCluster_EmptyTierRanksAsGoldLikeAgainstExplicitSilver(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "c1", Tier: "", Domain: "d1.com", PublishedAt: t0.Add(time.Hour)},
		{ArticleID: "a2", ClusterID: "c1", Tier: "silver", Domain: "d2.com", PublishedAt: t0},
	}

	got := editorialtrain.CapPerCluster(examples, 1)
	if len(got) != 1 || got[0].ArticleID != "a1" {
		t.Errorf("CapPerCluster(cap=1) = %+v, want only a1 (empty Tier ranks Gold-like, ahead of explicit silver, consistent with WriteManifest)", got)
	}
}

func TestCapPerCluster_DeferredFillWhenGroupExceedsCapAndDomainsRepeat(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", ClusterID: "c1", Tier: "gold", Domain: "same.com", PublishedAt: t0},
		{ArticleID: "a2", ClusterID: "c1", Tier: "gold", Domain: "same.com", PublishedAt: t0.Add(time.Hour)},
		{ArticleID: "a3", ClusterID: "c1", Tier: "gold", Domain: "same.com", PublishedAt: t0.Add(2 * time.Hour)},
	}

	got := editorialtrain.CapPerCluster(examples, 2)
	if len(got) != 2 {
		t.Errorf("CapPerCluster(cap=2) len = %d, want 2", len(got))
	}
	gotIDs := map[string]bool{}
	for _, e := range got {
		gotIDs[e.ArticleID] = true
	}
	if !gotIDs["a1"] || !gotIDs["a2"] {
		t.Errorf("CapPerCluster(cap=2) = %+v, want a1 + a2 (earliest ranked, via deferred-fill fallback when all domains repeat)", got)
	}
}
