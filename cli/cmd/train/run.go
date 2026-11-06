package train

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newRunCommand builds `train run`, the full-workflow orchestrator
// (#253 part 22): build -> analyze -> evaluate -> promote, aborting
// cleanly on any stage failure. --auto (#253 part 23) makes it check
// offline_training.retrain_triggers first: disabled
// (auto_enabled=false, the default) is an immediate, DB-untouched
// no-op; enabled but no threshold fired is also a no-op, just after a
// cheap signal-gathering read; only a fired threshold runs the full
// pipeline.
func newRunCommand() *cobra.Command {
	var force, auto bool
	var datasetPath, candidatesDir string
	var includeDB []string
	var maxPerCluster int
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the full safe-promotion workflow end-to-end: build, analyze, evaluate, promote",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if cfg.Model.Path == "" {
				return fmt.Errorf("train run: model.path is not configured")
			}
			if cfg.OfflineTraining.LLM.AutoLabelThreshold <= 0 {
				return fmt.Errorf("train run: offline_training.llm.auto_label_threshold must be configured above 0")
			}

			if auto && !cfg.OfflineTraining.RetrainTriggers.AutoEnabled {
				cmd.Println("auto training disabled (offline_training.retrain_triggers.auto_enabled=false)")
				return nil
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			var archivedDBs []*sql.DB
			defer func() {
				for _, archived := range archivedDBs {
					_ = archived.Close()
				}
			}()
			var archivedLabels []out.HumanLabelRepository
			for _, path := range includeDB {
				archive, openErr := infrasqlite.OpenReadOnly(path, cfg.SQLite.BusyTimeout())
				if openErr != nil {
					return openErr
				}
				archivedDBs = append(archivedDBs, archive)
				archivedLabels = append(archivedLabels, sqliteadapter.NewHumanLabelRepository(archive))
			}

			deps := trainwiring.RunDeps(db, cfg, archivedLabels)
			now := time.Now().UTC()
			opts, err := trainwiring.RunOptions(cfg, trainwiring.RunFlags{
				DatasetPath: datasetPath, CandidatesDir: candidatesDir, MaxPerCluster: maxPerCluster,
				Force: force, Auto: auto,
			}, now)
			if err != nil {
				return err
			}

			if auto {
				triggered, reasons, err := trainwiring.EvaluateAuto(cmd.Context(), deps, opts, cfg)
				if err != nil {
					return err
				}
				if !triggered {
					cmd.Println("no retrain triggered: no configured criteria were met")
					return nil
				}
				opts.TriggerReasons = reasons
			}

			outcome, err := trainrun.Run(cmd.Context(), deps, opts, now)
			if err != nil {
				return err
			}
			for _, warning := range outcome.Warnings {
				cmd.Println(warning)
			}
			cmd.Print(outcome.Report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"promote even if the candidate fails the offline_training.promotion criteria; "+
			"editorial_gates are a hard floor this flag cannot bypass — a candidate that fails them is never eligible for promotion, forced or not")
	cmd.Flags().BoolVar(&auto, "auto", false, "only run if offline_training.retrain_triggers.auto_enabled is true and a configured trigger fires")
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset.yml", "path to write the built dataset to")
	cmd.Flags().StringVar(&candidatesDir, "candidates", "models/editorial/candidates", "directory to write trained candidates to")
	cmd.Flags().IntVar(&maxPerCluster, "max-per-cluster", 1, "maximum number of examples kept from any one event cluster")
	cmd.Flags().StringSliceVar(&includeDB, "include-db", nil, "read-only SQLite snapshot whose human labels should be merged into Gold (repeatable)")
	return cmd
}
