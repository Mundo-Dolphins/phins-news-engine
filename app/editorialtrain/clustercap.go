package editorialtrain

import (
	"sort"
)

// CapPerCluster caps how many examples from the same non-empty
// ClusterID survive into the dataset, so one large story can't
// dominate word frequencies during training. Examples with an empty
// ClusterID are never capped -- each is already its own event.
// maxPerCluster <= 0 disables capping (pass-through, mirroring
// SplitConfig.NearDupThreshold's <= 0 convention elsewhere in this
// package).
//
// Within a cluster larger than maxPerCluster, candidates are ranked by
// (1) Tier: gold before silver, (2) PublishedAt ascending, then kept
// greedily up to maxPerCluster, preferring a Domain not yet
// represented among the picks; once every remaining candidate's Domain
// is already represented, the cap is filled by rank regardless of
// repeat domain. The returned slice preserves the relative order of
// surviving examples from the input.
//
// Note: Gold rows carry the ClusterID snapshotted at label time (from
// editorial_examples), while Silver rows carry the live ClusterID read
// from articles at export time -- so a cluster reassignment between
// labeling and export can split what was really one event across two
// cap groups here. That is inherent to Gold's snapshot design, not a
// bug in CapPerCluster itself.
func CapPerCluster(examples []TrainingExample, maxPerCluster int) []TrainingExample {
	if maxPerCluster <= 0 {
		return examples
	}

	byCluster := map[string][]TrainingExample{}
	var clusterOrder []string
	for _, e := range examples {
		if e.ClusterID == "" {
			continue
		}
		if _, seen := byCluster[e.ClusterID]; !seen {
			clusterOrder = append(clusterOrder, e.ClusterID)
		}
		byCluster[e.ClusterID] = append(byCluster[e.ClusterID], e)
	}

	kept := map[string]bool{} // ArticleID -> kept
	for _, clusterID := range clusterOrder {
		group := byCluster[clusterID]
		if len(group) <= maxPerCluster {
			for _, e := range group {
				kept[e.ArticleID] = true
			}
			continue
		}
		for _, e := range selectClusterRepresentatives(group, maxPerCluster) {
			kept[e.ArticleID] = true
		}
	}

	result := make([]TrainingExample, 0, len(examples))
	for _, e := range examples {
		if e.ClusterID == "" || kept[e.ArticleID] {
			result = append(result, e)
		}
	}
	return result
}

func tierRank(tier string) int {
	if isSilverTier(tier) {
		return 1
	}
	return 0
}

// selectClusterRepresentatives picks up to max examples from group,
// preferring Gold over Silver, then earliest PublishedAt, then a
// Domain not already represented among the picks -- falling back to
// pure rank order once every remaining candidate's Domain repeats one
// already picked.
func selectClusterRepresentatives(group []TrainingExample, max int) []TrainingExample {
	ranked := append([]TrainingExample{}, group...)
	sort.SliceStable(ranked, func(i, j int) bool {
		ti, tj := tierRank(ranked[i].Tier), tierRank(ranked[j].Tier)
		if ti != tj {
			return ti < tj
		}
		if !ranked[i].PublishedAt.Equal(ranked[j].PublishedAt) {
			return ranked[i].PublishedAt.Before(ranked[j].PublishedAt)
		}
		return ranked[i].ArticleID < ranked[j].ArticleID
	})

	seenDomain := map[string]bool{}
	var picked, deferred []TrainingExample
	for _, e := range ranked {
		if len(picked) == max {
			break
		}
		if e.Domain != "" && seenDomain[e.Domain] {
			deferred = append(deferred, e)
			continue
		}
		picked = append(picked, e)
		if e.Domain != "" {
			seenDomain[e.Domain] = true
		}
	}
	for _, e := range deferred {
		if len(picked) == max {
			break
		}
		picked = append(picked, e)
	}
	return picked
}
