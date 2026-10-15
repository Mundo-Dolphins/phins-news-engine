package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

func TestLogisticRegressionTrainer_Algorithm(t *testing.T) {
	tr := editorialtrain.LogisticRegressionTrainer{}
	if tr.Algorithm() != "logistic_regression" {
		t.Errorf("Algorithm() = %q, want %q", tr.Algorithm(), "logistic_regression")
	}
}

func TestLogisticRegressionTrainer_LearnsCorrectDirection(t *testing.T) {
	tr := editorialtrain.LogisticRegressionTrainer{Iterations: 300, LearningRate: 0.5, L2: 0.01}
	m, err := tr.Fit(nbDataset(), nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if m.Algorithm != "logistic_regression" {
		t.Errorf("Model.Algorithm = %q, want %q", m.Algorithm, "logistic_regression")
	}
	if m.Weights["uni:isotopes"] <= 0 {
		t.Errorf("weight for uni:isotopes = %v, want > 0", m.Weights["uni:isotopes"])
	}
	if m.Weights["uni:sponsored"] >= 0 {
		t.Errorf("weight for uni:sponsored = %v, want < 0", m.Weights["uni:sponsored"])
	}
	publishScore := editorial.Predict(m, editorial.FeatureVector{"uni:isotopes": 1})
	rejectScore := editorial.Predict(m, editorial.FeatureVector{"uni:sponsored": 1})
	if publishScore.Value <= rejectScore.Value {
		t.Errorf("publishScore.Value (%v) <= rejectScore.Value (%v)", publishScore.Value, rejectScore.Value)
	}
}

func TestLogisticRegressionTrainer_DeterministicNoRandomness(t *testing.T) {
	tr := editorialtrain.LogisticRegressionTrainer{Iterations: 100}
	m1, err := tr.Fit(nbDataset(), nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	m2, err := tr.Fit(nbDataset(), nbSchema(), 999) // different seed must not matter — batch GD has no randomness
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	for f, w := range m1.Weights {
		if m2.Weights[f] != w {
			t.Errorf("weight for %s differs across different seeds: %v vs %v — batch GD must be seed-independent", f, w, m2.Weights[f])
		}
	}
}

func TestLogisticRegressionTrainer_DefaultsApplyWhenZeroValued(t *testing.T) {
	tr := editorialtrain.LogisticRegressionTrainer{} // Iterations/LearningRate/L2 all zero
	if _, err := tr.Fit(nbDataset(), nbSchema(), 1); err != nil {
		t.Fatalf("Fit() with zero-valued config error = %v, want it to fall back to sane defaults", err)
	}
}

func TestLogisticRegressionTrainer_EmptyExamplesReturnsError(t *testing.T) {
	tr := editorialtrain.LogisticRegressionTrainer{}
	if _, err := tr.Fit(nil, nbSchema(), 1); err == nil {
		t.Error("Fit(nil examples) error = nil, want an error")
	}
}
