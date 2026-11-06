package train

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
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

			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}

			report, err := trainwiring.Evaluate(cmd.Context(), db, cfg, trainwiring.EvaluateFlags{
				DatasetPath: datasetPath, OutDir: outDir, Algorithms: algorithms, Seed: seed,
				AcceptTargetPrecision: acceptTargetPrecision, RejectTargetPrecision: rejectTargetPrecision,
				NearDupThreshold: nearDupThreshold, CostGrid: costGrid,
			}, time.Now().UTC())
			if err != nil {
				return err
			}

			if report.UnparseableSourceIDs > 0 {
				cmd.Printf("WARNING: %d of %d dataset example(s) have a source_id that does not parse as \"platform:identifier\" — those rows trained with no source_id feature (see editorialtrain.TrainingExample.toArticle)\n",
					report.UnparseableSourceIDs, report.DatasetSize)
			}
			cmd.Printf("excluded %d first-party official source example(s) from NB training\n", report.ExcludedOfficial)
			for _, w := range report.CostGrid {
				if !w.Found {
					cmd.Printf("%-20s cost-grid: no pair among the swept grid clears the promotion gates\n", w.Algorithm)
					continue
				}
				cmd.Printf("%-20s cost-grid winner: accept=%.2f reject=%.2f expected_cost=%.2f\n", w.Algorithm, w.Accept, w.Reject, w.ExpectedCost)
			}

			cmd.Printf("%-20s %10s %10s %10s %10s | %10s %10s %10s | %s\n",
				"algorithm", "val_prec", "val_rec", "val_f1", "val_prauc", "test_prec", "test_rec", "test_f1", "gates")
			if promoted := report.Promoted; promoted != nil {
				cmd.Printf("%-20s %10s %10s %10s %10s | %10.3f %10.3f %10.3f | %s\n",
					"(current promoted)", "-", "-", "-", "-",
					promoted.Overall.Precision(), promoted.Overall.Recall(), promoted.Overall.F1(),
					"n/a")
			}
			for _, r := range report.Results {
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
			cmd.Printf("\ncandidates written under %s — inspect with `validate-model`, then `train promote` the one you choose.\n", report.OutDir)
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
