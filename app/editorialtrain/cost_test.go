package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func TestExpectedCost_WeightsFalsePositivesAndFalseNegativesByCost(t *testing.T) {
	report := editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 10, FP: 3, FN: 2, TN: 20}}
	costs := editorialtrain.CostConfig{FalsePublishCost: 3, MissedNewsCost: 1}

	got := report.ExpectedCost(costs)
	want := 3.0*3 + 2.0*1 // FP*FalsePublishCost + FN*MissedNewsCost
	if got != want {
		t.Errorf("ExpectedCost() = %v, want %v", got, want)
	}
}

func TestExpectedCost_ZeroWhenNoErrors(t *testing.T) {
	report := editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 10, FP: 0, FN: 0, TN: 20}}
	if got := report.ExpectedCost(editorialtrain.CostConfig{FalsePublishCost: 3, MissedNewsCost: 1}); got != 0 {
		t.Errorf("ExpectedCost() = %v, want 0", got)
	}
}
