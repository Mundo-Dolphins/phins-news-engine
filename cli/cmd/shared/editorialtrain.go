package shared

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
)

// EditorialTrainConfig is the one TrainConfig every editorial training
// entrypoint (`train evaluate`, `train run`, `editorial cycle`) starts
// from, so split, pruning, threshold rule, ambiguous policy and the
// inference-population filter can never drift between them. Callers only
// override what is genuinely theirs (Algorithms, CLI-flag targets).
func EditorialTrainConfig(cfg *config.Config, schemaVersion string) (editorialtrain.TrainConfig, error) {
	mt := cfg.OfflineTraining.ModelTraining
	tc := editorialtrain.TrainConfig{
		Split: editorialtrain.SplitConfig{
			TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15, NearDupThreshold: 0.8,
			StratifyByLabel: mt.StratifySplitByLabel,
		},
		AmbiguousPolicy: cfg.Editorial.AmbiguousPolicy,
		Prune:           editorialtrain.PruneConfig{MinDocFreq: 0.01, MaxDocFreq: 0.9, MaxFeatures: 2000},
		Thresholds: editorialtrain.ThresholdConfig{
			AcceptTargetPrecision: 0.9, RejectTargetPrecision: 0.9,
			MaxFalsePositiveRate: mt.ThresholdMaxFalsePositiveRate,
		},
		Seed:           42,
		SchemaVersion:  schemaVersion,
		BinaryFeatures: mt.BinaryFeatures,
		SilverWeight:   mt.SilverWeight,
	}
	if mt.RelevantOnly {
		relevant, err := EditorialRelevanceFilter(cfg)
		if err != nil {
			return editorialtrain.TrainConfig{}, err
		}
		tc.Relevant = relevant
	}
	return tc, nil
}

// EditorialRelevanceFilter reports whether a labeled example would pass
// ProcessCycle's relevance stage (Step 3) with the current dictionary and
// source lists — i.e. whether it would ever reach the editorial
// classifier in production. It mirrors that step's three branches:
// trusted-source bypass, source-context decision, plain decision, all on
// normalized title/summary text.
func EditorialRelevanceFilter(cfg *config.Config) (func(editorialtrain.TrainingExample) bool, error) {
	d, err := dictionaryfile.Load(cfg.Dictionary.Path)
	if err != nil {
		return nil, fmt.Errorf("editorial relevance filter: load dictionary %s: %w", cfg.Dictionary.Path, err)
	}
	matcher, err := dictionary.Prepare(d)
	if err != nil {
		return nil, fmt.Errorf("editorial relevance filter: prepare dictionary: %w", err)
	}
	cascade := cfg.DedupCascade()
	if !cascade.Classification.Enabled {
		// Relevance gating is off in production, so everything reaches the
		// editorial classifier.
		return func(editorialtrain.TrainingExample) bool { return true }, nil
	}
	textCfg := cascade.Normalization.Text
	trusted, context := cascade.Classification.TrustedSourceIDs, cascade.Classification.ContextSourceIDs

	return func(ex editorialtrain.TrainingExample) bool {
		if trusted[ex.SourceID] {
			return true
		}
		title := normalization.Normalize(ex.Title, textCfg).ForClassification
		body := normalization.Normalize(ex.Body, textCfg).ForClassification
		if context[ex.SourceID] {
			return classification.DecideWithSourceContext(title, body, matcher).Related
		}
		return classification.Decide(title, body, matcher).Related
	}, nil
}
