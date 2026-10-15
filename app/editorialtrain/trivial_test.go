package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func TestTrivialTrainer_AlwaysPredictsMajorityLabel(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		labeledExample("p1", "anything", "publish"),
		labeledExample("p2", "anything else", "publish"),
		labeledExample("r1", "something", "reject"),
	}
	schema := editorialtrain.FeatureSchema{Version: "fs-test", Features: []string{"uni:anything"}}

	model, err := editorialtrain.TrivialTrainer{}.Fit(examples, schema, 0)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if model.Algorithm != "trivial_majority" {
		t.Errorf("Algorithm = %q, want %q", model.Algorithm, "trivial_majority")
	}
	if len(model.Weights) != 0 {
		t.Errorf("Weights = %v, want empty (majority-only bias)", model.Weights)
	}
	if model.Bias <= 0 {
		t.Errorf("Bias = %v, want > 0 (publish is the majority label, 2 vs 1)", model.Bias)
	}
}

func TestTrivialTrainer_DeterministicRegardlessOfSeed(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		labeledExample("r1", "x", "reject"), labeledExample("r2", "y", "reject"), labeledExample("p1", "z", "publish"),
	}
	schema := editorialtrain.FeatureSchema{Version: "fs-test"}
	m1, _ := editorialtrain.TrivialTrainer{}.Fit(examples, schema, 1)
	m2, _ := editorialtrain.TrivialTrainer{}.Fit(examples, schema, 999)
	if m1.Bias != m2.Bias {
		t.Errorf("Bias differs by seed: %v vs %v, want identical (majority label doesn't depend on seed)", m1.Bias, m2.Bias)
	}
	if m1.Bias >= 0 {
		t.Errorf("Bias = %v, want < 0 (reject is the majority label, 2 vs 1)", m1.Bias)
	}
}
