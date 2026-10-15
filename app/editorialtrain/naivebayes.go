package editorialtrain

import (
	"fmt"
	"math"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// NaiveBayesTrainer fits a multinomial Naive Bayes classifier with
// Laplace (add-one) smoothing, then reduces it to editorial.Model's
// bias+linear-weights shape: NB's log-odds decision function,
// Σ v_f * log(P(f|publish)/P(f|reject)) + log-prior-odds, is already
// exactly that form — no separate runtime representation needed.
//
// Binary counts each feature once per example (Bernoulli-style presence)
// instead of by its value, which keeps long texts and repeated character
// n-grams from dominating; the fitted Model carries BinaryFeatures so
// Predict binarizes identically at inference.
//
// SilverWeight scales every Silver-tier example's contribution to the
// class prior and to the per-feature counts; Gold
// examples always count 1. Zero means 1 (unweighted); values outside
// (0, 1] are rejected by Fit.
type NaiveBayesTrainer struct {
	Binary       bool
	SilverWeight float64
}

// exampleWeight is ex's training weight: silverWeight for Silver rows and
// 1 for everything else, including an empty Tier (see isSilverTier).
func exampleWeight(ex TrainingExample, silverWeight float64) float64 {
	if isSilverTier(ex.Tier) {
		return silverWeight
	}
	return 1
}

func (NaiveBayesTrainer) Algorithm() string { return "naive_bayes" }

func (t NaiveBayesTrainer) Fit(examples []TrainingExample, schema FeatureSchema, _ int64) (editorial.Model, error) {
	if len(examples) == 0 {
		return editorial.Model{}, fmt.Errorf("editorialtrain: naive bayes: no training examples")
	}

	silverWeight := t.SilverWeight
	if silverWeight == 0 {
		silverWeight = 1
	}
	if silverWeight < 0 || silverWeight > 1 {
		return editorial.Model{}, fmt.Errorf("editorialtrain: naive bayes: silver weight %v out of range (0, 1]", t.SilverWeight)
	}

	numPublish, numReject := 0.0, 0.0
	for _, ex := range examples {
		if ex.Label == "publish" {
			numPublish += exampleWeight(ex, silverWeight)
		} else {
			numReject += exampleWeight(ex, silverWeight)
		}
	}
	bias := math.Log(numPublish+1) - math.Log(numReject+1)

	return editorial.Model{
		Algorithm: "naive_bayes", FeatureSchemaVersion: schema.Version,
		Bias: bias, Weights: nbLogRatios(examples, schema, t.Binary, silverWeight), BinaryFeatures: t.Binary,
	}, nil
}

// nbLogRatios computes each schema feature's Naive Bayes log-count ratio
// log(P(f|publish)/P(f|reject)), with Laplace (add-one) smoothing — the
// same quantity NaiveBayesTrainer uses directly as its linear weights, and
// NBSVMTrainer uses to transform features before running Pegasos.
func nbLogRatios(examples []TrainingExample, schema FeatureSchema, binary bool, silverWeight float64) map[string]float64 {
	countPublish := make(map[string]float64, len(schema.Features))
	countReject := make(map[string]float64, len(schema.Features))
	totalPublish, totalReject := 0.0, 0.0

	for _, ex := range examples {
		fv := restrictToSchema(ex.Features(), schema)
		if binary {
			for f, v := range fv {
				if v != 0 {
					fv[f] = 1
				}
			}
		}
		w := exampleWeight(ex, silverWeight)
		if ex.Label == "publish" {
			for f, v := range fv {
				countPublish[f] += v * w
				totalPublish += v * w
			}
		} else {
			for f, v := range fv {
				countReject[f] += v * w
				totalReject += v * w
			}
		}
	}

	vocab := float64(len(schema.Features))
	ratios := make(map[string]float64, len(schema.Features))
	for _, f := range schema.Features {
		pPublish := (countPublish[f] + 1) / (totalPublish + vocab)
		pReject := (countReject[f] + 1) / (totalReject + vocab)
		ratios[f] = math.Log(pPublish) - math.Log(pReject)
	}
	return ratios
}
