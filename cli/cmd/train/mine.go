package train

import (
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainmine"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newMineCommand builds `train mine`:
// scanning stored articles not yet in Gold for strong NB/LLM
// disagreement, standalone from `train run` (which also invokes this
// same logic as part of its pipeline — see trainrun's Task 7 wiring).
func newMineCommand() *cobra.Command {
	var limitOverride int
	cmd := &cobra.Command{
		Use:   "mine",
		Short: "Scan stored articles for strong NB/LLM disagreement (hard-negative/hard-positive mining)",
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			batchLimit := cfg.OfflineTraining.Mining.BatchLimit
			if limitOverride > 0 {
				batchLimit = limitOverride
			}

			llm := llmclient.New(shared.OfflineTrainingLLMClientConfig(cfg.OfflineTraining.LLM))

			result, err := trainmine.Mine(cmd.Context(), cfg.Model.Path,
				sqliteadapter.NewArticleRepository(db), llm, sqliteadapter.NewHardExampleRepository(db),
				trainmine.Options{
					BatchLimit: batchLimit, MinNBConfidence: cfg.OfflineTraining.Mining.MinNBConfidence,
					LLMAutoLabelThreshold: cfg.OfflineTraining.LLM.AutoLabelThreshold,
				}, time.Now().UTC())
			if err != nil {
				return err
			}
			if result.Skipped {
				cmd.Println(result.SkipReason)
				return nil
			}
			cmd.Printf("scored %d articles: %d hard negatives, %d hard positives\n",
				result.ArticlesScored, result.HardNegativeCount, result.HardPositiveCount)
			return nil
		},
	}
	cmd.Flags().IntVar(&limitOverride, "limit", 0, "override offline_training.mining.batch_limit for this run")
	return cmd
}
