// app/eventrulecycle/gate_test.go
package eventrulecycle_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
)

func report(tp, fp int, purity float64, over int) clustereval.Report {
	return clustereval.Report{Pairwise: clustereval.PairwiseMetrics{TP: tp, FP: fp}, Purity: purity, OverGroupedClusters: over}
}

func TestEvaluateGate(t *testing.T) {
	before := report(9, 1, 0.9, 2) // precision 0.9
	cases := []struct {
		name     string
		after    clustereval.Report
		ok       bool
		contains []string
	}{
		{"equal passes", report(9, 1, 0.9, 2), true, nil},
		{"improvement passes", report(10, 0, 0.95, 1), true, nil},
		{"precision drop", report(8, 2, 0.9, 2), false, []string{"precision", "0.9000", "0.8000"}},
		{"purity drop", report(9, 1, 0.85, 2), false, []string{"purity", "0.9000", "0.8500"}},
		{"over-grouped rise", report(9, 1, 0.9, 3), false, []string{"over-grouped", "2", "3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := eventrulecycle.EvaluateGate(before, c.after)
			if got.OK != c.ok {
				t.Fatalf("OK = %v, want %v (%s)", got.OK, c.ok, got.Reason)
			}
			for _, s := range c.contains {
				if !strings.Contains(got.Reason, s) {
					t.Errorf("reason %q lacks %q", got.Reason, s)
				}
			}
			if got.Before.Purity != before.Purity || got.After.Purity != c.after.Purity {
				t.Errorf("reports not carried: %+v", got)
			}
		})
	}
}

func TestEvaluateGate_ToleratesFloatNoise(t *testing.T) {
	before := report(9, 1, 0.9, 2)
	after := before
	after.Purity = 0.9 - 1e-12
	if got := eventrulecycle.EvaluateGate(before, after); !got.OK {
		t.Fatalf("float noise failed the gate: %s", got.Reason)
	}
}
