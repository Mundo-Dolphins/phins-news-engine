package trainwiring

import (
	"path/filepath"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

// CycleTrainingConfig assembles the training step configuration of
// `editorial cycle` from cfg: the trusted and excluded sources, the dataset
// file next to the operational database, the candidates directory next to
// the promoted model, and the training and gate configs. schemaVersion names
// the candidates' feature schema; an error comes from TrainConfig.
func CycleTrainingConfig(cfg *config.Config, excludedSourceIDs map[string]bool, schemaVersion string) (editorialcycle.TrainingConfig, error) {
	trustedSourceIDs := make(map[string]bool, len(cfg.Classification.TrustedSourceIDs))
	for _, id := range cfg.Classification.TrustedSourceIDs {
		trustedSourceIDs[id] = true
	}
	trainCfg, err := TrainConfig(cfg, schemaVersion)
	if err != nil {
		return editorialcycle.TrainingConfig{}, err
	}
	return editorialcycle.TrainingConfig{
		TrustedSourceIDs:  trustedSourceIDs,
		ExcludedSourceIDs: excludedSourceIDs,
		DatasetPath:       filepath.Join(filepath.Dir(cfg.SQLite.Path), "editorial_cycle_dataset.yml"),
		OutDir:            filepath.Join(filepath.Dir(cfg.Model.Path), "candidates"),
		TrainCfg:          trainCfg,
		GateCfg:           GateConfig(cfg),
		PromotedModelPath: cfg.Model.Path,
	}, nil
}
