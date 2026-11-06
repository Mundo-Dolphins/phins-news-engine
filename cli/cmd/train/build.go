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

// newBuildCommand builds `train build`, which assembles the Gold+Silver
// training dataset — renamed from `train export` (#253 part 22).
func newBuildCommand() *cobra.Command {
	var outPath string
	var maxPerCluster int
	var includeDB []string
	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Build a training dataset from Gold and approved Silver examples",
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
			report, err := trainwiring.BuildDataset(cmd.Context(), db, cfg,
				trainwiring.BuildFlags{OutPath: outPath, MaxPerCluster: maxPerCluster, IncludeDB: includeDB}, time.Now().UTC())
			if err != nil {
				return err
			}
			if report.UnresolvedConflicts > 0 {
				cmd.Printf("WARNING: %d article(s) have an unresolved legacy label conflict and are excluded from this export — see `label conflicts` and `label resolve-conflict`\n", report.UnresolvedConflicts)
			}
			cmd.Printf("built %d examples to %s (%d gold, %d silver; %d dropped by --max-per-cluster)\n",
				report.Examples, report.OutPath, report.Gold, report.Silver, report.Dropped)
			return nil
		},
	}
	buildCmd.Flags().StringVar(&outPath, "out", "data/editorial_dataset.yml", "path to write the built dataset")
	buildCmd.Flags().IntVar(&maxPerCluster, "max-per-cluster", 1, "maximum number of examples kept from any one event cluster, to prevent one story from dominating word frequencies; <= 0 disables capping")
	buildCmd.Flags().StringSliceVar(&includeDB, "include-db", nil, "read-only SQLite snapshot whose human labels should be merged into Gold (repeatable)")
	return buildCmd
}
