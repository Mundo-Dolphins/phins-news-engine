package editorialtrain

import "sort"

// SplitConfig controls Split's time-ordered train/validate/test cut.
// Ratios need not sum to exactly 1.0 (Split floors each count and puts any
// remainder in the test partition), but should for a predictable split.
type SplitConfig struct {
	TrainRatio    float64
	ValidateRatio float64
	TestRatio     float64
	// NearDupThreshold is the Jaccard similarity (on 3-word title+body
	// shingles) at or above which two examples are treated as
	// near-duplicates and forced into the same partition, in addition to
	// exact-duplicate and same-ClusterID grouping (which always apply).
	// <= 0 disables near-dup grouping only — exact-dup/ClusterID grouping
	// is unconditional.
	NearDupThreshold float64
	// StratifyByLabel applies the time-ordered ratio cut to each label
	// separately instead of to the whole dataset, so a label that was
	// collected in one burst (e.g. a manual reject-labeling campaign)
	// still lands in validate and test. Leakage grouping still applies
	// afterwards and can move a group to its earliest member's partition.
	StratifyByLabel bool
}

const (
	partitionTrain = iota
	partitionValidate
	partitionTest
)

// Split partitions d by PublishedAt (oldest first) according to cfg's
// ratios, then reassigns any group of examples sharing a non-empty
// ClusterID entirely to the partition of the group's earliest-published
// member — so no partition ever sees a duplicate/near-duplicate/other
// version of an event another partition already saw. An empty ClusterID
// is never grouped (each such example is its own singleton group).
func Split(d Dataset, cfg SplitConfig) (train, validate, test Dataset) {
	examples := append([]TrainingExample{}, d.Examples...)
	sort.SliceStable(examples, func(i, j int) bool {
		if !examples[i].PublishedAt.Equal(examples[j].PublishedAt) {
			return examples[i].PublishedAt.Before(examples[j].PublishedAt)
		}
		return examples[i].ArticleID < examples[j].ArticleID
	})

	partitionOf := make(map[string]int, len(examples)) // ArticleID -> partition
	if cfg.StratifyByLabel {
		byLabel := map[string][]TrainingExample{}
		for _, e := range examples { // examples is time-sorted, so each label's slice is too
			byLabel[e.Label] = append(byLabel[e.Label], e)
		}
		for _, group := range byLabel {
			assignTimeCut(group, cfg, partitionOf)
		}
	} else {
		assignTimeCut(examples, cfg, partitionOf)
	}

	groups := leakageGroups(examples, cfg.NearDupThreshold)
	groupEarliestPartition := map[string]int{}
	for _, e := range examples { // examples is time-sorted, so the first member of a group seen here is its earliest
		group := groups[e.ArticleID]
		if _, seen := groupEarliestPartition[group]; !seen {
			groupEarliestPartition[group] = partitionOf[e.ArticleID]
		}
	}
	for _, e := range examples {
		partitionOf[e.ArticleID] = groupEarliestPartition[groups[e.ArticleID]]
	}

	for _, e := range examples {
		switch partitionOf[e.ArticleID] {
		case partitionTrain:
			train.Examples = append(train.Examples, e)
		case partitionValidate:
			validate.Examples = append(validate.Examples, e)
		default:
			test.Examples = append(test.Examples, e)
		}
	}
	return train, validate, test
}

// assignTimeCut assigns time-sorted examples to train/validate/test by
// cfg's ratios, recording the result in partitionOf.
func assignTimeCut(examples []TrainingExample, cfg SplitConfig, partitionOf map[string]int) {
	n := len(examples)
	nTrain := int(float64(n) * cfg.TrainRatio)
	nValidate := int(float64(n) * cfg.ValidateRatio)
	for i, e := range examples {
		switch {
		case i < nTrain:
			partitionOf[e.ArticleID] = partitionTrain
		case i < nTrain+nValidate:
			partitionOf[e.ArticleID] = partitionValidate
		default:
			partitionOf[e.ArticleID] = partitionTest
		}
	}
}
