package editorialtrain

import (
	"fmt"
	"math"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// TrivialTrainer fits a majority-class baseline — zero feature weights,
// a bias set purely from the training split's label proportions (the
// same Laplace-smoothed log-odds NaiveBayesTrainer's bias uses, so
// Predict's logistic squash lands consistently below/above 50 for a
// skewed split), so it always predicts whichever label was more common
// in training regardless of an article's content. Included in train
// run's comparison table as an explicit "can the real candidates beat
// doing nothing clever at all" floor (see the design doc §4) —
// modeltraining.SelectWinner excludes it from ever actually winning.
type TrivialTrainer struct{}

func (TrivialTrainer) Algorithm() string { return "trivial_majority" }

func (TrivialTrainer) Fit(examples []TrainingExample, schema FeatureSchema, _ int64) (editorial.Model, error) {
	if len(examples) == 0 {
		return editorial.Model{}, fmt.Errorf("editorialtrain: trivial majority: no training examples")
	}
	numPublish, numReject := 0, 0
	for _, ex := range examples {
		if ex.Label == "publish" {
			numPublish++
		} else {
			numReject++
		}
	}
	bias := math.Log(float64(numPublish)+1) - math.Log(float64(numReject)+1)
	return editorial.Model{
		Algorithm: "trivial_majority", FeatureSchemaVersion: schema.Version,
		Bias: bias, Weights: map[string]float64{},
	}, nil
}
