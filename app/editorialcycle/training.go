// Package editorialcycle (this file): the post-review training
// continuation — export the current dataset, train every candidate,
// evaluate gates, and select a winner. See modeltraining.SelectWinner for
// the ranking policy (highest PR-AUC among gate-passers).
package editorialcycle

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// TrainingDeps are the ports RunTraining reads from and writes through.
type TrainingDeps struct {
	Labels   out.HumanLabelRepository
	Registry out.ModelRegistryRepository
}

// TrainingConfig configures one RunTraining call.
type TrainingConfig struct {
	TrustedSourceIDs  map[string]bool
	ExcludedSourceIDs map[string]bool
	DatasetPath       string // where to write the generated dataset file
	OutDir            string // where RunAndRegister writes candidate directories
	TrainCfg          editorialtrain.TrainConfig
	GateCfg           editorialtrain.GateConfig
	PromotedModelPath string
}

// TrainingResult is the outcome of one RunTraining call.
type TrainingResult struct {
	Results   []modeltraining.Result
	Winner    modeltraining.Result
	HasWinner bool
}

// RunTraining exports the current human-labeled dataset, writes it to
// cfg.DatasetPath, trains every requested candidate algorithm, and
// selects a winner (see modeltraining.SelectWinner). Returns an error
// only for genuine failures (no labels, training failure); "no candidate
// passed gates" is HasWinner=false, not an error.
func RunTraining(ctx context.Context, deps TrainingDeps, cfg TrainingConfig) (TrainingResult, error) {
	examples, err := editorialtrain.ExportDataset(ctx, deps.Labels, cfg.TrustedSourceIDs, cfg.ExcludedSourceIDs)
	if err != nil {
		return TrainingResult{}, fmt.Errorf("editorialcycle: export dataset: %w", err)
	}
	if err := editorialtrain.SaveDataset(cfg.DatasetPath, examples); err != nil {
		return TrainingResult{}, fmt.Errorf("editorialcycle: save dataset: %w", err)
	}
	dataset, err := editorialtrain.LoadDataset(cfg.DatasetPath)
	if err != nil {
		return TrainingResult{}, fmt.Errorf("editorialcycle: reload dataset: %w", err)
	}

	promoted := modeltraining.LoadPromotedReport(cfg.PromotedModelPath)
	results, err := modeltraining.RunAndRegister(ctx, modeltraining.Deps{Registry: deps.Registry}, dataset, cfg.TrainCfg, cfg.OutDir, cfg.GateCfg, promoted)
	if err != nil {
		return TrainingResult{}, fmt.Errorf("editorialcycle: train: %w", err)
	}

	winner, ok := modeltraining.SelectWinner(results)
	return TrainingResult{Results: results, Winner: winner, HasWinner: ok}, nil
}
