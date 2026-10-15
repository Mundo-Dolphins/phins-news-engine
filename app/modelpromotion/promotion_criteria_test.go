package modelpromotion

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func metricsWithPrecisionF1(tp, fp, fn, tn int) editorialtrain.Report {
	return editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: tp, FP: fp, FN: fn, TN: tn}}
}

func TestEvaluatePromotionCriteria(t *testing.T) {
	tests := []struct {
		name          string
		candidate     editorialtrain.Report
		promoted      *editorialtrain.Report
		cfg           PromotionCriteria
		wantPassed    bool
		wantFailCount int
	}{
		{
			name:       "no baseline, candidate clears min precision",
			candidate:  metricsWithPrecisionF1(90, 5, 10, 895), // precision 90/95=0.947
			promoted:   nil,
			cfg:        PromotionCriteria{MinPrecision: 0.9, MaxPrecisionDrop: 0.02, MinF1Improvement: 0.01},
			wantPassed: true,
		},
		{
			name:       "no baseline, candidate below min precision",
			candidate:  metricsWithPrecisionF1(80, 20, 10, 890), // precision 0.8
			promoted:   nil,
			cfg:        PromotionCriteria{MinPrecision: 0.9},
			wantPassed: false, wantFailCount: 1,
		},
		{
			name:       "precision drop exceeds max",
			candidate:  metricsWithPrecisionF1(90, 10, 10, 890),           // precision 0.9
			promoted:   reportPtr(metricsWithPrecisionF1(97, 3, 10, 890)), // precision 0.97
			cfg:        PromotionCriteria{MinPrecision: 0.5, MaxPrecisionDrop: 0.02, MinF1Improvement: -1},
			wantPassed: false, wantFailCount: 1,
		},
		{
			name:       "F1 improvement insufficient",
			candidate:  metricsWithPrecisionF1(90, 10, 10, 890), // precision .9, recall .9, F1 .9
			promoted:   reportPtr(metricsWithPrecisionF1(90, 10, 10, 890)),
			cfg:        PromotionCriteria{MinPrecision: 0.5, MaxPrecisionDrop: 1, MinF1Improvement: 0.05},
			wantPassed: false, wantFailCount: 1,
		},
		{
			name:       "all pass with baseline",
			candidate:  metricsWithPrecisionF1(95, 5, 5, 895),              // precision .95, recall .95, F1 .95
			promoted:   reportPtr(metricsWithPrecisionF1(90, 10, 10, 890)), // precision .9, F1 .9
			cfg:        PromotionCriteria{MinPrecision: 0.5, MaxPrecisionDrop: 1, MinF1Improvement: 0.01},
			wantPassed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passed, failures := evaluatePromotionCriteria(tt.candidate, tt.promoted, tt.cfg)
			if passed != tt.wantPassed {
				t.Errorf("passed = %v, want %v (failures=%v)", passed, tt.wantPassed, failures)
			}
			if len(failures) != tt.wantFailCount && !tt.wantPassed {
				t.Errorf("len(failures) = %d, want %d (failures=%v)", len(failures), tt.wantFailCount, failures)
			}
		})
	}
}

func reportPtr(r editorialtrain.Report) *editorialtrain.Report { return &r }
