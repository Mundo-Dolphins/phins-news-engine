package train

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
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

			result, err := trainwiring.Mine(cmd.Context(), db, cfg, limitOverride, time.Now().UTC())
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
