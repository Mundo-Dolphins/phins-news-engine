package train

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/humanlabels"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func newBackfillRejectsCommand() *cobra.Command {
	var limit int
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "backfill-rejects",
		Short: "Label real pipeline-rejected articles as reject training examples",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// root command (and this subcommand's flag vars) are a package-level
			// singleton shared across every CLI invocation within the same
			// process -- Cobra does not reset flag values between Execute()
			// calls. A real `phins-news-engine-tools` invocation is always its
			// own process, so this never matters there, but any caller that
			// invokes this command repeatedly in one process (tests via
			// testsupport.Execute, or an in-process automation script) would
			// otherwise see a prior run's --dry-run/--limit silently leak
			// into the next one. Same defensive reset pattern as
			// dictionary.go's mine-candidates and editorialcycle.go's
			// cycle command.
			defer func() { limit = 20000; dryRun = false }()
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

			articles := sqliteadapter.NewArticleRepository(db)
			labels := sqliteadapter.NewHumanLabelRepository(db)

			rep, err := humanlabels.BackfillRejects(cmd.Context(), articles, labels, limit, dryRun, time.Now().UTC())
			if err != nil {
				return err
			}
			cmd.Printf("backfill-rejects: %d candidate(s) found, %d labeled, %d skipped (already labeled) (dry-run=%v)\n",
				rep.Candidates, rep.Labeled, rep.Skipped, dryRun)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20000, "maximum number of articles to backfill in one run")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report candidate counts without writing any labels")
	return cmd
}
