package editorialtrain

import (
	"fmt"
	"math"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// LogisticRegressionTrainer fits an L2-regularized logistic regression by
// full-batch gradient descent — deterministic (seed is accepted only for
// Trainer interface uniformity and is never read). Zero
// values for Iterations/LearningRate/L2 fall back to defaults.
type LogisticRegressionTrainer struct {
	Iterations   int
	LearningRate float64
	L2           float64
}

func (LogisticRegressionTrainer) Algorithm() string { return "logistic_regression" }

func (t LogisticRegressionTrainer) Fit(examples []TrainingExample, schema FeatureSchema, _ int64) (editorial.Model, error) {
	if len(examples) == 0 {
		return editorial.Model{}, fmt.Errorf("editorialtrain: logistic regression: no training examples")
	}
	iterations := t.Iterations
	if iterations <= 0 {
		iterations = 200
	}
	lr := t.LearningRate
	if lr <= 0 {
		lr = 0.1
	}

	type row struct {
		fv editorial.FeatureVector
		y  float64
	}
	rows := make([]row, len(examples))
	for i, ex := range examples {
		y := 0.0
		if ex.Label == "publish" {
			y = 1.0
		}
		rows[i] = row{restrictToSchema(ex.Features(), schema), y}
	}

	weights := make(map[string]float64, len(schema.Features))
	for _, f := range schema.Features {
		weights[f] = 0
	}
	bias := 0.0
	n := float64(len(rows))

	for iter := 0; iter < iterations; iter++ {
		gradW := make(map[string]float64, len(schema.Features))
		gradB := 0.0
		for _, r := range rows {
			z := bias
			for f, v := range r.fv {
				z += weights[f] * v
			}
			predicted := 1 / (1 + math.Exp(-z))
			errTerm := predicted - r.y
			for f, v := range r.fv {
				gradW[f] += errTerm * v
			}
			gradB += errTerm
		}
		for _, f := range schema.Features {
			weights[f] -= lr * (gradW[f]/n + t.L2*weights[f])
		}
		bias -= lr * (gradB / n)
	}

	return editorial.Model{
		Algorithm: "logistic_regression", FeatureSchemaVersion: schema.Version,
		Bias: bias, Weights: weights,
	}, nil
}
