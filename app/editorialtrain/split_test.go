package editorialtrain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func exampleAt(id string, days int, cluster string) editorialtrain.TrainingExample {
	return editorialtrain.TrainingExample{
		ArticleID: id, ClusterID: cluster,
		Title: "Article " + id, Body: "Content for article " + id,
		PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days),
	}
}

func TestSplit_RatiosNoGroups(t *testing.T) {
	var examples []editorialtrain.TrainingExample
	for i := 0; i < 10; i++ {
		examples = append(examples, exampleAt(string(rune('a'+i)), i, ""))
	}
	d := editorialtrain.Dataset{Examples: examples}
	train, validate, test := editorialtrain.Split(d, editorialtrain.SplitConfig{TrainRatio: 0.6, ValidateRatio: 0.2, TestRatio: 0.2})

	if len(train.Examples) != 6 {
		t.Errorf("len(train) = %d, want 6", len(train.Examples))
	}
	if len(validate.Examples) != 2 {
		t.Errorf("len(validate) = %d, want 2", len(validate.Examples))
	}
	if len(test.Examples) != 2 {
		t.Errorf("len(test) = %d, want 2", len(test.Examples))
	}

	// Every train example must be published before every validate example,
	// and every validate example before every test example (time-ordered cut).
	for _, tr := range train.Examples {
		for _, va := range validate.Examples {
			if !tr.PublishedAt.Before(va.PublishedAt) {
				t.Errorf("train example %s (%v) not before validate example %s (%v)", tr.ArticleID, tr.PublishedAt, va.ArticleID, va.PublishedAt)
			}
		}
	}
}

func TestSplit_KeepsWholeClusterTogether(t *testing.T) {
	// c1's two members straddle the day-6/day-7 train/validate boundary a
	// plain 60/20/20 cut over 10 examples would draw.
	examples := []editorialtrain.TrainingExample{
		exampleAt("a", 0, ""), exampleAt("b", 1, ""), exampleAt("c", 2, ""), exampleAt("d", 3, ""),
		exampleAt("e", 4, ""), exampleAt("f", 5, "c1"), exampleAt("g", 6, "c1"),
		exampleAt("h", 7, ""), exampleAt("i", 8, ""), exampleAt("j", 9, ""),
	}
	d := editorialtrain.Dataset{Examples: examples}
	train, validate, _ := editorialtrain.Split(d, editorialtrain.SplitConfig{TrainRatio: 0.6, ValidateRatio: 0.2, TestRatio: 0.2})

	trainIDs := map[string]bool{}
	for _, e := range train.Examples {
		trainIDs[e.ArticleID] = true
	}
	validateIDs := map[string]bool{}
	for _, e := range validate.Examples {
		validateIDs[e.ArticleID] = true
	}

	fInTrain, gInTrain := trainIDs["f"], trainIDs["g"]
	fInValidate, gInValidate := validateIDs["f"], validateIDs["g"]
	if fInTrain != gInTrain || fInValidate != gInValidate || (!fInTrain && !fInValidate) {
		t.Errorf("cluster c1's members f/g were split across partitions: f in train=%v validate=%v, g in train=%v validate=%v", fInTrain, fInValidate, gInTrain, gInValidate)
	}
}

func TestSplit_NoClusterAppearsInMoreThanOnePartition(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		exampleAt("a", 0, "c1"), exampleAt("b", 1, "c1"), exampleAt("c", 2, "c1"),
		exampleAt("d", 3, "c2"), exampleAt("e", 8, "c2"),
		exampleAt("f", 9, ""),
	}
	d := editorialtrain.Dataset{Examples: examples}
	train, validate, test := editorialtrain.Split(d, editorialtrain.SplitConfig{TrainRatio: 0.5, ValidateRatio: 0.25, TestRatio: 0.25})

	clusterPartitions := map[string]map[string]bool{}
	for name, ds := range map[string]editorialtrain.Dataset{"train": train, "validate": validate, "test": test} {
		for _, e := range ds.Examples {
			if e.ClusterID == "" {
				continue
			}
			if clusterPartitions[e.ClusterID] == nil {
				clusterPartitions[e.ClusterID] = map[string]bool{}
			}
			clusterPartitions[e.ClusterID][name] = true
		}
	}
	for cluster, partitions := range clusterPartitions {
		if len(partitions) > 1 {
			t.Errorf("cluster %s appears in %d partitions, want 1: %v", cluster, len(partitions), partitions)
		}
	}
}

func TestSplit_StratifyByLabelSpreadsBurstLabeledRejectsAcrossPartitions(t *testing.T) {
	// Rejects were all labeled in one early burst (days 0-9), publishes
	// cover days 0-99 — a plain time cut leaves validate/test reject-free.
	var examples []editorialtrain.TrainingExample
	for i := 0; i < 100; i++ {
		ex := exampleAt(fmt.Sprintf("p%02d", i), i, "")
		ex.Label = "publish"
		examples = append(examples, ex)
	}
	for i := 0; i < 20; i++ {
		ex := exampleAt(fmt.Sprintf("r%02d", i), i/2, "")
		ex.Label = "reject"
		examples = append(examples, ex)
	}
	d := editorialtrain.Dataset{Examples: examples}

	count := func(ds editorialtrain.Dataset, label string) int {
		n := 0
		for _, e := range ds.Examples {
			if e.Label == label {
				n++
			}
		}
		return n
	}

	_, plainValidate, plainTest := editorialtrain.Split(d, editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15})
	if count(plainValidate, "reject")+count(plainTest, "reject") != 0 {
		t.Fatalf("fixture sanity: plain time split put rejects in validate/test; the fixture no longer reproduces the burst problem")
	}

	train, validate, test := editorialtrain.Split(d, editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15, StratifyByLabel: true})
	if got := count(train, "reject"); got != 14 {
		t.Errorf("train rejects = %d, want 14", got)
	}
	if got := count(validate, "reject"); got != 3 {
		t.Errorf("validate rejects = %d, want 3", got)
	}
	if got := count(test, "reject"); got != 3 {
		t.Errorf("test rejects = %d, want 3", got)
	}
	if got := count(validate, "publish"); got != 15 {
		t.Errorf("validate publishes = %d, want 15", got)
	}
	// Within one label the cut is still time-ordered.
	for _, tr := range train.Examples {
		for _, te := range test.Examples {
			if tr.Label == te.Label && tr.PublishedAt.After(te.PublishedAt) {
				t.Errorf("train %s (%v) is after same-label test %s (%v)", tr.ArticleID, tr.PublishedAt, te.ArticleID, te.PublishedAt)
			}
		}
	}
}
