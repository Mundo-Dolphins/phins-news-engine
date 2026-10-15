package train

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newEvaluateCommand builds `train evaluate`, which fits and evaluates
// every requested candidate algorithm from a built dataset — renamed
// from `train run` (#253 part 22; the "run" verb now belongs to the new
// full-workflow orchestrator, Task 9 of this plan).
func newEvaluateCommand() *cobra.Command {
	var datasetPath, outDir string
	var algorithms []string
	var seed int64
	var acceptTargetPrecision, rejectTargetPrecision float64
	var nearDupThreshold float64
	var costGrid string
	evaluateCmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Fit and evaluate every requested candidate algorithm from a built dataset",
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() { algorithms = nil }()

			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return err
			}
			if dataset.UnparseableSourceIDCount > 0 {
				cmd.Printf("WARNING: %d of %d dataset example(s) have a source_id that does not parse as \"platform:identifier\" — those rows trained with no source_id feature (see editorialtrain.TrainingExample.toArticle)\n",
					dataset.UnparseableSourceIDCount, len(dataset.Examples))
			}

			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			excludedSources := map[string]bool{}
			for _, id := range cfg.Editorial.ModelBypassSourceIDs {
				excludedSources[id] = true
			}
			for _, id := range cfg.Editorial.UnconditionalPublishSourceIDs {
				excludedSources[id] = true
			}
			originalCount := len(dataset.Examples)
			dataset = dataset.ExcludeSources(excludedSources)
			cmd.Printf("excluded %d first-party official source example(s) from NB training\n", originalCount-len(dataset.Examples))
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			registry := sqliteadapter.NewModelRegistryRepository(db)

			schemaVersion := fmt.Sprintf("fs-%d", time.Now().UTC().UnixNano())
			trainCfg, err := shared.EditorialTrainConfig(cfg, schemaVersion)
			if err != nil {
				return err
			}
			trainCfg.Split.NearDupThreshold = nearDupThreshold
			trainCfg.Thresholds.AcceptTargetPrecision, trainCfg.Thresholds.RejectTargetPrecision = acceptTargetPrecision, rejectTargetPrecision
			trainCfg.Seed = seed
			trainCfg.Algorithms = algorithms
			gateCfg := editorialtrain.GateConfig{
				MinPrecision: cfg.EditorialGates.MinPrecision, MaxRecallRegression: cfg.EditorialGates.MaxRecallRegression,
				MinRegressionBaselineExamples: cfg.EditorialGates.MinRegressionBaselineExamples,
				MinPRAUC:                      cfg.EditorialGates.MinPRAUC, MaxFalsePositiveRate: cfg.EditorialGates.MaxFalsePositiveRate,
				MinPerSourcePrecision: cfg.EditorialGates.MinPerSourcePrecision, MinSourceExamples: cfg.EditorialGates.MinSourceExamples,
				ExcludedSources:      cfg.EditorialGates.ExcludedSources,
				MaxArtifactSizeBytes: cfg.EditorialGates.MaxArtifactSizeBytes, MaxInferenceNanos: cfg.EditorialGates.MaxInferenceNanos,
			}
			promoted, err := modeltraining.EvaluatePromotedModel(cfg.Model.Path, dataset, trainCfg)
			if err != nil {
				return err
			}

			results, err := modeltraining.RunAndRegister(cmd.Context(), modeltraining.Deps{Registry: registry}, dataset, trainCfg, outDir, gateCfg, promoted)
			if err != nil {
				return err
			}

			if costGrid != "" {
				if cfg.EditorialCosts.FalsePublishCost <= 0 && cfg.EditorialCosts.MissedNewsCost <= 0 {
					return fmt.Errorf("train evaluate: --cost-grid requires editorial_costs to be configured in settings (false_publish_cost and missed_news_cost are both <= 0)")
				}
				values, err := parseCostGrid(costGrid)
				if err != nil {
					return err
				}
				_, validate, _ := editorialtrain.SplitForTraining(dataset, trainCfg)
				costs := editorialtrain.CostConfig{FalsePublishCost: cfg.EditorialCosts.FalsePublishCost, MissedNewsCost: cfg.EditorialCosts.MissedNewsCost}

				for _, r := range results {
					if r.Candidate.Model.Algorithm == "trivial_majority" {
						continue
					}
					artifactSize, err := modeltraining.DirSize(r.Dir)
					if err != nil {
						return err
					}
					var best editorial.Thresholds
					bestCost := math.Inf(1)
					found := false
					for _, accept := range values {
						for _, reject := range values {
							th := editorialtrain.SelectThresholds(r.Candidate.Model, validate.Examples, editorialtrain.ThresholdConfig{AcceptTargetPrecision: accept, RejectTargetPrecision: reject})
							m := r.Candidate.Model
							m.Thresholds = th
							report := editorialtrain.Evaluate(m, validate.Examples, nil, cfg.Editorial.AmbiguousPolicy)
							gates := editorialtrain.EvaluateGates(report, promoted, artifactSize, gateCfg)
							if !gates.Passed {
								continue
							}
							cost := report.ExpectedCost(costs)
							if cost < bestCost {
								bestCost = cost
								best = th
								found = true
							}
						}
					}
					if !found {
						cmd.Printf("%-20s cost-grid: no pair among the swept grid clears the promotion gates\n", r.Candidate.Model.Algorithm)
						continue
					}
					cmd.Printf("%-20s cost-grid winner: accept=%.2f reject=%.2f expected_cost=%.2f\n", r.Candidate.Model.Algorithm, best.Accept, best.Reject, bestCost)
				}
			}

			cmd.Printf("%-20s %10s %10s %10s %10s | %10s %10s %10s | %s\n",
				"algorithm", "val_prec", "val_rec", "val_f1", "val_prauc", "test_prec", "test_rec", "test_f1", "gates")
			if promoted != nil {
				cmd.Printf("%-20s %10s %10s %10s %10s | %10.3f %10.3f %10.3f | %s\n",
					"(current promoted)", "-", "-", "-", "-",
					promoted.Overall.Precision(), promoted.Overall.Recall(), promoted.Overall.F1(),
					"n/a")
			}
			for _, r := range results {
				c := r.Candidate
				gateStatus := "✅ pass"
				if !r.Gates.Passed {
					gateStatus = fmt.Sprintf("❌ %s", strings.Join(r.Gates.Failures, "; "))
				}
				cmd.Printf("%-20s %10.3f %10.3f %10.3f %10.3f | %10.3f %10.3f %10.3f | %s\n",
					c.Model.Algorithm,
					c.ValidateReport.Overall.Precision(), c.ValidateReport.Overall.Recall(), c.ValidateReport.Overall.F1(), c.ValidateReport.PRAUC,
					c.TestReport.Overall.Precision(), c.TestReport.Overall.Recall(), c.TestReport.Overall.F1(),
					gateStatus)
			}
			cmd.Printf("\ncandidates written under %s — inspect with `validate-model`, then `train promote` the one you choose.\n", outDir)
			return nil
		},
	}
	evaluateCmd.Flags().StringVar(&datasetPath, "dataset", "", "path to the built training dataset (required)")
	evaluateCmd.Flags().StringVar(&outDir, "out", "models/editorial/candidates", "directory to write one subdirectory per trained candidate")
	evaluateCmd.Flags().StringSliceVar(&algorithms, "algorithm", nil, "algorithms to train (repeatable; default: all four)")
	evaluateCmd.Flags().Int64Var(&seed, "seed", 42, "seed for the Pegasos-based trainers (linear_svm, nbsvm)")
	evaluateCmd.Flags().Float64Var(&acceptTargetPrecision, "accept-target-precision", 0.9, "target precision SelectThresholds aims for on the accept side of the validation split")
	evaluateCmd.Flags().Float64Var(&rejectTargetPrecision, "reject-target-precision", 0.9, "target precision SelectThresholds aims for on the reject side of the validation split")
	evaluateCmd.Flags().Float64Var(&nearDupThreshold, "near-dup-threshold", 0.8, "Jaccard similarity threshold on 3-word title+body shingles for near-duplicate grouping before train/validate/test split; <= 0 disables near-dup grouping")
	evaluateCmd.Flags().StringVar(&costGrid, "cost-grid", "", "comma-separated target-precision values to grid-sweep on both accept and reject sides, printing the pair minimizing expected cost (requires editorial_costs in config); empty disables the sweep")
	_ = evaluateCmd.MarkFlagRequired("dataset")
	return evaluateCmd
}

func parseCostGrid(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	values := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("train evaluate: invalid --cost-grid value %q: %w", p, err)
		}
		values = append(values, v)
	}
	return values, nil
}
