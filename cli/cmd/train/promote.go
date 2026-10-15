package train

import (
	"fmt"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// promotionCriteriaFrom converts config.PromotionConfig into
// modelpromotion.PromotionCriteria — the cmd layer's job
// (application packages never import infrastructure/config).
func promotionCriteriaFrom(c config.PromotionConfig) modelpromotion.PromotionCriteria {
	return modelpromotion.PromotionCriteria{
		MinPrecision: c.MinPrecision, MaxPrecisionDrop: c.MaxPrecisionDrop, MinF1Improvement: c.MinF1Improvement,
	}
}

// newPromoteCommand builds `train promote`, moved from the top-level
// `promote-model` (#253 part 22) — same underlying modelpromotion.Promote,
// now also checking offline_training.promotion criteria alongside
// editorial_gates.
func newPromoteCommand() *cobra.Command {
	var version string
	var force bool
	promoteCmd := &cobra.Command{
		Use:   "promote <candidate-dir>",
		Short: "Atomically promote a candidate editorial-classifier model to production, gated by quality thresholds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			candidateDir := args[0]
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if cfg.Model.Path == "" {
				return fmt.Errorf("train promote: model.path is not configured")
			}
			candidateManifest, _, _, err := editorialmodel.Inspect(candidateDir)
			if err != nil {
				return err
			}
			if candidateManifest.Algorithm != "naive_bayes" {
				return fmt.Errorf("train promote: candidate algorithm %q is not eligible; production classifier must remain naive_bayes", candidateManifest.Algorithm)
			}

			gateCfg := editorialtrain.GateConfig{
				MinPrecision: cfg.EditorialGates.MinPrecision, MaxRecallRegression: cfg.EditorialGates.MaxRecallRegression,
				MinRegressionBaselineExamples: cfg.EditorialGates.MinRegressionBaselineExamples,
				MinPRAUC:                      cfg.EditorialGates.MinPRAUC, MaxFalsePositiveRate: cfg.EditorialGates.MaxFalsePositiveRate,
				MinPerSourcePrecision: cfg.EditorialGates.MinPerSourcePrecision, MinSourceExamples: cfg.EditorialGates.MinSourceExamples,
				ExcludedSources:      cfg.EditorialGates.ExcludedSources,
				MaxArtifactSizeBytes: cfg.EditorialGates.MaxArtifactSizeBytes, MaxInferenceNanos: cfg.EditorialGates.MaxInferenceNanos,
			}
			promoted := modeltraining.LoadPromotedReport(cfg.Model.Path)
			gateResult, err := modelpromotion.CheckGates(candidateDir, gateCfg, promotionCriteriaFrom(cfg.OfflineTraining.Promotion), promoted)
			if err != nil {
				return err
			}
			if !gateResult.Passed {
				for _, f := range gateResult.Failures {
					cmd.Printf("gate failed: %s\n", f)
				}
				if !force {
					return fmt.Errorf("train promote: candidate fails %d gate(s); re-run with --force to override", len(gateResult.Failures))
				}
				cmd.Println("--force: promoting despite the above gate failure(s)")
			}

			candidateManifest, candidateSchema, _, err := editorialmodel.Inspect(candidateDir)
			if err != nil {
				return err
			}
			v := version
			if v == "" {
				v = candidateManifest.Version
			}

			if _, promotedSchema, _, err := editorialmodel.Inspect(cfg.Model.Path); err == nil {
				jaccard, added, removed := editorialtrain.SchemaOverlap(
					editorialtrain.FeatureSchema{Version: candidateSchema.Version, Features: candidateSchema.Features},
					editorialtrain.FeatureSchema{Version: promotedSchema.Version, Features: promotedSchema.Features},
				)
				cmd.Printf("vocabulary overlap with current promoted model: %.1f%% (added=%d removed=%d)\n", jaccard*100, added, removed)
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			uow := sqliteadapter.NewUnitOfWork(db)
			registry := sqliteadapter.NewModelRegistryRepository(db)

			if err := modelpromotion.Promote(cmd.Context(), modelpromotion.Deps{Registry: registry, UOW: uow}, candidateDir, v, cfg.Model.Path); err != nil {
				return fmt.Errorf("train promote: %w", err)
			}

			cmd.Printf("promoted %s to %s\n", v, cfg.Model.Path)
			return nil
		},
	}
	promoteCmd.Flags().StringVar(&version, "version", "", "version to assign (default: the candidate's own manifest version)")
	promoteCmd.Flags().BoolVar(&force, "force", false, "promote even if the candidate fails one or more quality gates")
	return promoteCmd
}
