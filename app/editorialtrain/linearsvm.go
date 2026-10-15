package editorialtrain

import (
	"fmt"
	"math/rand"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// LinearSVMTrainer fits a linear SVM via Pegasos (hinge-loss subgradient
// descent with per-epoch example shuffling) — the shuffling is the only
// source of randomness anywhere in editorialtrain, seeded from Fit's seed
// parameter for reproducibility. Zero-valued Epochs/Lambda fall back to
// defaults.
type LinearSVMTrainer struct {
	Epochs int
	Lambda float64
}

func (LinearSVMTrainer) Algorithm() string { return "linear_svm" }

func (t LinearSVMTrainer) Fit(examples []TrainingExample, schema FeatureSchema, seed int64) (editorial.Model, error) {
	if len(examples) == 0 {
		return editorial.Model{}, fmt.Errorf("editorialtrain: linear svm: no training examples")
	}
	epochs, lambda := svmDefaults(t.Epochs, t.Lambda)

	rows := make([]svmRow, len(examples))
	for i, ex := range examples {
		rows[i] = svmRow{restrictToSchema(ex.Features(), schema), labelSign(ex.Label)}
	}

	bias, weights := pegasos(rows, schema, epochs, lambda, seed)
	return editorial.Model{
		Algorithm: "linear_svm", FeatureSchemaVersion: schema.Version,
		Bias: bias, Weights: weights,
	}, nil
}

// svmRow is one training row already reduced to (schema-restricted
// feature vector, {-1,+1} label) — the shape both LinearSVMTrainer and
// NBSVMTrainer feed into pegasos.
type svmRow struct {
	fv editorial.FeatureVector
	y  float64
}

func labelSign(label string) float64 {
	if label == "publish" {
		return 1
	}
	return -1
}

func svmDefaults(epochs int, lambda float64) (int, float64) {
	if epochs <= 0 {
		epochs = 50
	}
	if lambda <= 0 {
		lambda = 0.01
	}
	return epochs, lambda
}

// pegasos runs Pegasos hinge-loss subgradient descent over rows for
// epochs full passes, reshuffled (seeded by seed) each pass.
func pegasos(rows []svmRow, schema FeatureSchema, epochs int, lambda float64, seed int64) (bias float64, weights map[string]float64) {
	weights = make(map[string]float64, len(schema.Features))
	for _, f := range schema.Features {
		weights[f] = 0
	}

	rng := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic, seeded PRNG for reproducible training shuffles, not security-sensitive
	step := 0
	for epoch := 0; epoch < epochs; epoch++ {
		for _, idx := range rng.Perm(len(rows)) {
			step++
			eta := 1 / (lambda * float64(step))
			r := rows[idx]

			z := bias
			for f, v := range r.fv {
				z += weights[f] * v
			}
			margin := r.y * z

			for _, f := range schema.Features {
				weights[f] *= 1 - eta*lambda
			}
			if margin < 1 {
				for f, v := range r.fv {
					weights[f] += eta * r.y * v
				}
				bias += eta * r.y
			}
		}
	}
	return bias, weights
}
