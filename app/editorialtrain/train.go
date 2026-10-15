package editorialtrain

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
)

// TrainConfig configures one Train() run: how to split the dataset, how
// to prune the feature schema, the validation-set target precision
// threshold selection aims for, the seed Pegasos-based trainers use, and
// which of the five candidate algorithms to actually fit (nil/empty means
// all five — "no decidir de antemano cuál gana", per the prompt).
type TrainConfig struct {
	Split              SplitConfig
	Prune              PruneConfig
	Thresholds         ThresholdConfig
	AmbiguousPolicy    string
	Seed               int64
	Algorithms         []string
	SchemaVersion      string
	LogisticRegression LogisticRegressionTrainer
	LinearSVM          LinearSVMTrainer
	NBSVM              NBSVMTrainer
	VetoCfg            *editorialveto.Config
	// Relevant, when non-nil, keeps only examples that would reach the
	// editorial classifier in production (e.g. ones the relevance stage
	// would not already reject). Applied before splitting by
	// SplitForTraining, so training, thresholds and every report see the
	// same population. nil keeps every example.
	Relevant func(TrainingExample) bool
	// BinaryFeatures trains naive_bayes on feature presence (see
	// NaiveBayesTrainer.Binary). Other algorithms are unaffected.
	BinaryFeatures bool
	// SilverWeight is naive_bayes's Silver-tier example weight (see
	// NaiveBayesTrainer.SilverWeight); 0 means unweighted.
	SilverWeight      float64
	ThresholdOverride *editorial.Thresholds
}

// Candidate is one algorithm's fully-evaluated output: the fitted model
// (with thresholds already selected on the validation split), its feature
// schema, and both the validation report (used to select thresholds) and
// the test report (informational only — test is never used to choose
// anything, avoiding leakage).
type Candidate struct {
	Model          editorial.Model
	Schema         FeatureSchema
	ValidateReport Report
	TestReport     Report
	DatasetHash    string
}

var allAlgorithms = []string{"naive_bayes", "logistic_regression", "linear_svm", "nbsvm", "trivial_majority"}

// Train splits d, builds a pruned feature schema from the training split
// only, fits every requested algorithm (default: all five), selects each
// one's thresholds on the validation split, and reports validation+test
// metrics for each — never picking a winner itself (see Task 21's `train
// run`, which prints Train's output as a comparison table for a human to
// choose from via `promote-model`).
func Train(d Dataset, cfg TrainConfig) ([]Candidate, error) {
	train, validate, test := SplitForTraining(d, cfg)
	schema := BuildSchema(train, cfg.Prune, cfg.SchemaVersion)
	datasetHash := d.Hash()

	algorithms := cfg.Algorithms
	if len(algorithms) == 0 {
		algorithms = allAlgorithms
	}

	var candidates []Candidate
	for _, name := range algorithms {
		trainer, err := resolveTrainer(name, cfg)
		if err != nil {
			return nil, err
		}

		model, err := trainer.Fit(train.Examples, schema, cfg.Seed)
		if err != nil {
			return nil, fmt.Errorf("editorialtrain: fit %s: %w", name, err)
		}

		if cfg.AmbiguousPolicy == "reject" && cfg.Thresholds.MaxFalsePositiveRate > 0 {
			model.Thresholds = SelectAcceptThresholdByFPR(model, validate.Examples, cfg.Thresholds.MaxFalsePositiveRate)
		} else {
			model.Thresholds = SelectThresholds(model, validate.Examples, cfg.Thresholds)
		}
		if cfg.ThresholdOverride != nil {
			model.Thresholds = *cfg.ThresholdOverride
		}

		candidates = append(candidates, Candidate{
			Model:          model,
			Schema:         schema,
			ValidateReport: Evaluate(model, validate.Examples, cfg.VetoCfg, cfg.AmbiguousPolicy),
			TestReport:     Evaluate(model, test.Examples, cfg.VetoCfg, cfg.AmbiguousPolicy),
			DatasetHash:    datasetHash,
		})
	}
	return candidates, nil
}

// SplitForTraining applies cfg.Relevant, then Split with cfg.Split — the
// exact partitions Train fits and evaluates on. Callers that re-derive a
// partition (the promoted-model baseline, the cost-grid sweep) must use
// this rather than Split so they compare on the same population.
func SplitForTraining(d Dataset, cfg TrainConfig) (train, validate, test Dataset) {
	if cfg.Relevant != nil {
		kept := make([]TrainingExample, 0, len(d.Examples))
		for _, ex := range d.Examples {
			if cfg.Relevant(ex) {
				kept = append(kept, ex)
			}
		}
		d = Dataset{Examples: kept}
	}

	// Silver (LLM-proposed) labels only ever train: validate picks the
	// thresholds and test drives the promotion gates, so both must measure
	// the model against human (Gold) labels, never the LLM against itself.
	// Gold is split on its own so Silver volume cannot shift its time cut.
	var gold, silver []TrainingExample
	for _, ex := range d.Examples {
		if isSilverTier(ex.Tier) {
			silver = append(silver, ex)
		} else {
			gold = append(gold, ex)
		}
	}
	train, validate, test = Split(Dataset{Examples: gold}, cfg.Split)
	if len(silver) == 0 {
		return train, validate, test
	}

	// A Silver example from the same event as a held-out Gold example
	// would leak that event into train.
	heldOut := make(map[string]bool)
	for _, part := range []Dataset{validate, test} {
		for _, ex := range part.Examples {
			heldOut[ex.ArticleID] = true
			if ex.ClusterID != "" {
				heldOut["cluster:"+ex.ClusterID] = true
			}
		}
	}
	trainExamples := append([]TrainingExample{}, train.Examples...)
	for _, ex := range silver {
		if heldOut[ex.ArticleID] || (ex.ClusterID != "" && heldOut["cluster:"+ex.ClusterID]) {
			continue
		}
		trainExamples = append(trainExamples, ex)
	}
	return Dataset{Examples: trainExamples}, validate, test
}

func resolveTrainer(name string, cfg TrainConfig) (Trainer, error) {
	switch name {
	case "naive_bayes":
		return NaiveBayesTrainer{Binary: cfg.BinaryFeatures, SilverWeight: cfg.SilverWeight}, nil
	case "logistic_regression":
		return cfg.LogisticRegression, nil
	case "linear_svm":
		return cfg.LinearSVM, nil
	case "nbsvm":
		return cfg.NBSVM, nil
	case "trivial_majority":
		return TrivialTrainer{}, nil
	default:
		return nil, fmt.Errorf("editorialtrain: unknown algorithm %q (want one of %v)", name, allAlgorithms)
	}
}
