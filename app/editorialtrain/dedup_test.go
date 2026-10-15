package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func ex(id, title, body string) editorialtrain.TrainingExample {
	return editorialtrain.TrainingExample{ArticleID: id, Title: title, Body: body, Label: "publish"}
}

func TestSplit_GroupsExactDuplicatesTogether(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		ex("a1", "Isotopes sign veteran cornerback", "The Isotopes announced a two-year deal Tuesday."),
		ex("a2", "ISOTOPES SIGN VETERAN CORNERBACK", "  the isotopes announced a two-year deal tuesday.  "),
		ex("a3", "Completely unrelated article about training camp", "Camp opens Monday with a full roster."),
	}
	train, validate, test := editorialtrain.Split(editorialtrain.Dataset{Examples: examples}, editorialtrain.SplitConfig{TrainRatio: 0.34, ValidateRatio: 0.33, TestRatio: 0.33})

	partitionOf := map[string]string{}
	for _, e := range train.Examples {
		partitionOf[e.ArticleID] = "train"
	}
	for _, e := range validate.Examples {
		partitionOf[e.ArticleID] = "validate"
	}
	for _, e := range test.Examples {
		partitionOf[e.ArticleID] = "test"
	}

	if partitionOf["a1"] != partitionOf["a2"] {
		t.Fatalf("a1 (%s) and a2 (%s) are exact duplicates (case/whitespace only) but landed in different partitions", partitionOf["a1"], partitionOf["a2"])
	}
}

func TestSplit_GroupsNearDuplicatesTogetherWhenThresholdConfigured(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		ex("n1", "Isotopes sign veteran cornerback to two year contract", "The Springfield Isotopes announced Tuesday they signed a veteran cornerback to a two year contract worth eighteen million dollars."),
		ex("n2", "Isotopes sign veteran cornerback to a two year contract", "The Springfield Isotopes announced Tuesday they signed a veteran cornerback to a two year contract worth eighteen million."),
		ex("n3", "Completely unrelated training camp story", "Camp opens Monday with a full roster and new drills for everyone."),
	}
	train, validate, test := editorialtrain.Split(editorialtrain.Dataset{Examples: examples},
		editorialtrain.SplitConfig{TrainRatio: 0.34, ValidateRatio: 0.33, TestRatio: 0.33, NearDupThreshold: 0.6})

	partitionOf := map[string]string{}
	for _, e := range train.Examples {
		partitionOf[e.ArticleID] = "train"
	}
	for _, e := range validate.Examples {
		partitionOf[e.ArticleID] = "validate"
	}
	for _, e := range test.Examples {
		partitionOf[e.ArticleID] = "test"
	}

	if partitionOf["n1"] != partitionOf["n2"] {
		t.Fatalf("n1 (%s) and n2 (%s) are near-duplicates (threshold 0.6) but landed in different partitions", partitionOf["n1"], partitionOf["n2"])
	}
}

func TestSplit_ZeroNearDupThresholdDisablesNearDupGrouping(t *testing.T) {
	// Two clearly-different-but-somewhat-overlapping articles must NOT be
	// forced together when NearDupThreshold is left at its zero value —
	// this is the backward-compatibility guarantee existing callers rely
	// on.
	examples := []editorialtrain.TrainingExample{
		ex("d1", "Isotopes sign veteran cornerback to two year deal", "The Springfield Isotopes signed a cornerback Tuesday to a two year deal."),
		ex("d2", "Isotopes waive veteran linebacker in roster move", "The Springfield Isotopes waived a linebacker Tuesday as a separate roster move."),
	}
	// With a high train ratio and only 2 examples, without any grouping
	// force they are free to land in different partitions (sorted by
	// PublishedAt, both zero -> sorted by ArticleID, d1 then d2).
	train, _, test := editorialtrain.Split(editorialtrain.Dataset{Examples: examples},
		editorialtrain.SplitConfig{TrainRatio: 0.5, ValidateRatio: 0, TestRatio: 0.5})

	if len(train.Examples) != 1 || len(test.Examples) != 1 {
		t.Fatalf("got train=%d test=%d, want 1/1 (no forced grouping with NearDupThreshold=0)", len(train.Examples), len(test.Examples))
	}
}
