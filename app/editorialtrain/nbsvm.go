package editorialtrain

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// NBSVMTrainer scales each feature by its Naive Bayes log-count ratio
// (nbLogRatios) before running the same Pegasos optimizer LinearSVMTrainer
// uses, then folds the ratios back into the learned weights
// (weight_f = svmWeight_f * ratio_f) — since w·(r⊙x) = (w⊙r)·x, the
// result is an ordinary linear model over the original feature space, no
// runtime-side transform required.
type NBSVMTrainer struct {
	Epochs int
	Lambda float64
}

func (NBSVMTrainer) Algorithm() string { return "nbsvm" }

func (t NBSVMTrainer) Fit(examples []TrainingExample, schema FeatureSchema, seed int64) (editorial.Model, error) {
	if len(examples) == 0 {
		return editorial.Model{}, fmt.Errorf("editorialtrain: nbsvm: no training examples")
	}
	epochs, lambda := svmDefaults(t.Epochs, t.Lambda)
	ratios := nbLogRatios(examples, schema, false, 1)

	rows := make([]svmRow, len(examples))
	for i, ex := range examples {
		raw := restrictToSchema(ex.Features(), schema)
		transformed := make(editorial.FeatureVector, len(raw))
		for f, v := range raw {
			transformed[f] = v * ratios[f]
		}
		rows[i] = svmRow{transformed, labelSign(ex.Label)}
	}

	svmBias, svmWeights := pegasos(rows, schema, epochs, lambda, seed)

	weights := make(map[string]float64, len(schema.Features))
	for _, f := range schema.Features {
		weights[f] = svmWeights[f] * ratios[f]
	}

	return editorial.Model{
		Algorithm: "nbsvm", FeatureSchemaVersion: schema.Version,
		Bias: svmBias, Weights: weights,
	}, nil
}
