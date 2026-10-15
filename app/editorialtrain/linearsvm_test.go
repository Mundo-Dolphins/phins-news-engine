package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

func TestLinearSVMTrainer_Algorithm(t *testing.T) {
	tr := editorialtrain.LinearSVMTrainer{}
	if tr.Algorithm() != "linear_svm" {
		t.Errorf("Algorithm() = %q, want %q", tr.Algorithm(), "linear_svm")
	}
}

func TestLinearSVMTrainer_LearnsCorrectDirection(t *testing.T) {
	tr := editorialtrain.LinearSVMTrainer{Epochs: 100, Lambda: 0.01}
	m, err := tr.Fit(nbDataset(), nbSchema(), 42)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if m.Algorithm != "linear_svm" {
		t.Errorf("Model.Algorithm = %q, want %q", m.Algorithm, "linear_svm")
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

func TestLinearSVMTrainer_ReproducibleWithFixedSeed(t *testing.T) {
	tr := editorialtrain.LinearSVMTrainer{Epochs: 50, Lambda: 0.01}
	m1, err := tr.Fit(nbDataset(), nbSchema(), 7)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	m2, err := tr.Fit(nbDataset(), nbSchema(), 7)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if m1.Bias != m2.Bias {
		t.Errorf("Bias differs across identical-seed Fit() calls: %v vs %v", m1.Bias, m2.Bias)
	}
	for f, w := range m1.Weights {
		if m2.Weights[f] != w {
			t.Errorf("weight for %s differs across identical-seed Fit() calls: %v vs %v", f, w, m2.Weights[f])
		}
	}
}

func TestLinearSVMTrainer_DefaultsApplyWhenZeroValued(t *testing.T) {
	tr := editorialtrain.LinearSVMTrainer{}
	if _, err := tr.Fit(nbDataset(), nbSchema(), 1); err != nil {
		t.Fatalf("Fit() with zero-valued config error = %v, want it to fall back to sane defaults", err)
	}
}

func TestLinearSVMTrainer_EmptyExamplesReturnsError(t *testing.T) {
	tr := editorialtrain.LinearSVMTrainer{}
	if _, err := tr.Fit(nil, nbSchema(), 1); err == nil {
		t.Error("Fit(nil examples) error = nil, want an error")
	}
}
