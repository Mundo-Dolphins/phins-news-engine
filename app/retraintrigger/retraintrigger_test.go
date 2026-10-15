package retraintrigger_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/retraintrigger"
)

func TestEvaluate_AllDisabledNeverTriggers(t *testing.T) {
	got := retraintrigger.Evaluate(retraintrigger.Config{}, retraintrigger.Signals{
		NewGoldSinceLastRun: 1000, NewSilverSinceLastRun: 1000,
		DisagreementRate: 1.0, ManualErrorRate: 1.0, NovelVocabularyCount: 1000, HardExampleCount: 1000,
	})
	if got.Triggered {
		t.Errorf("Evaluate() = %+v, want Triggered=false when every threshold is 0", got)
	}
	if len(got.Reasons) != 0 {
		t.Errorf("Reasons = %v, want empty", got.Reasons)
	}
}

func TestEvaluate_EachConditionIndividually(t *testing.T) {
	tests := []struct {
		name       string
		cfg        retraintrigger.Config
		signals    retraintrigger.Signals
		wantReason string
	}{
		{
			name:       "new gold examples at threshold",
			cfg:        retraintrigger.Config{MinNewGoldExamples: 100},
			signals:    retraintrigger.Signals{NewGoldSinceLastRun: 142},
			wantReason: "142 new Gold examples (threshold 100)",
		},
		{
			name:       "new silver examples at threshold",
			cfg:        retraintrigger.Config{MinNewSilverExamples: 50},
			signals:    retraintrigger.Signals{NewSilverSinceLastRun: 60},
			wantReason: "60 new Silver examples (threshold 50)",
		},
		{
			name:       "disagreement rate exceeds max",
			cfg:        retraintrigger.Config{MaxDisagreementRate: 0.05},
			signals:    retraintrigger.Signals{DisagreementRate: 0.08},
			wantReason: "disagreement rate 0.080 exceeds max 0.050",
		},
		{
			name:       "manual error rate exceeds max",
			cfg:        retraintrigger.Config{MaxManualErrorRate: 0.2},
			signals:    retraintrigger.Signals{ManualErrorRate: 0.35},
			wantReason: "manual error rate 0.350 exceeds max 0.200",
		},
		{
			name:       "novel vocabulary count at threshold",
			cfg:        retraintrigger.Config{NovelVocabularyThreshold: 10},
			signals:    retraintrigger.Signals{NovelVocabularyCount: 12},
			wantReason: "12 novel-vocabulary queue items (threshold 10)",
		},
		{
			name:       "hard example count at threshold",
			cfg:        retraintrigger.Config{MinHardExampleCount: 5},
			signals:    retraintrigger.Signals{HardExampleCount: 7},
			wantReason: "7 hard-example candidates (threshold 5)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retraintrigger.Evaluate(tt.cfg, tt.signals)
			if !got.Triggered {
				t.Fatalf("Evaluate() = %+v, want Triggered=true", got)
			}
			if !reflect.DeepEqual(got.Reasons, []string{tt.wantReason}) {
				t.Errorf("Reasons = %v, want [%q]", got.Reasons, tt.wantReason)
			}
		})
	}
}

func TestEvaluate_BelowThresholdDoesNotTrigger(t *testing.T) {
	got := retraintrigger.Evaluate(
		retraintrigger.Config{MinNewGoldExamples: 100, MaxDisagreementRate: 0.05},
		retraintrigger.Signals{NewGoldSinceLastRun: 99, DisagreementRate: 0.05},
	)
	if got.Triggered {
		t.Errorf("Evaluate() = %+v, want Triggered=false (99 < 100, 0.05 is not > 0.05)", got)
	}
}

func TestEvaluate_MultipleConditionsCombine(t *testing.T) {
	got := retraintrigger.Evaluate(
		retraintrigger.Config{MinNewGoldExamples: 100, NovelVocabularyThreshold: 10},
		retraintrigger.Signals{NewGoldSinceLastRun: 142, NovelVocabularyCount: 12, DisagreementRate: 0},
	)
	if !got.Triggered {
		t.Fatal("Evaluate() Triggered = false, want true")
	}
	want := []string{"142 new Gold examples (threshold 100)", "12 novel-vocabulary queue items (threshold 10)"}
	if !reflect.DeepEqual(got.Reasons, want) {
		t.Errorf("Reasons = %v, want %v", got.Reasons, want)
	}
}
