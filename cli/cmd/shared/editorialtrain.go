package shared

import (
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

// EditorialTrainConfig is the one TrainConfig every editorial training
// entrypoint starts from. It delegates to trainwiring.TrainConfig.
func EditorialTrainConfig(cfg *config.Config, schemaVersion string) (editorialtrain.TrainConfig, error) {
	return trainwiring.TrainConfig(cfg, schemaVersion)
}

// EditorialRelevanceFilter reports whether a labeled example would pass
// ProcessCycle's relevance stage. It delegates to trainwiring.RelevanceFilter.
func EditorialRelevanceFilter(cfg *config.Config) (func(editorialtrain.TrainingExample) bool, error) {
	return trainwiring.RelevanceFilter(cfg)
}
