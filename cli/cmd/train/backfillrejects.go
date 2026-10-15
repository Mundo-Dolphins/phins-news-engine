package train

import (
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// Only rejected_quality is a direct editorial-quality outcome. The other
// terminal statuses, such as rejected_not_related and retained_no_publish,
// occur upstream in relevance/clustering, so those articles never reached
// the editorial classifier and cannot teach its publish/reject boundary.
var backfillStatuses = []article.Status{
	article.StatusRejectedQuality,
}

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

			candidates, err := articles.ListByStatusUnlabeled(cmd.Context(), backfillStatuses, limit)
			if err != nil {
				return fmt.Errorf("train backfill-rejects: %w", err)
			}

			labeled := 0
			skipped := 0
			now := time.Now().UTC()
			for _, a := range candidates {
				if dryRun {
					continue
				}
				example := training.Example{
					ArticleID: a.ID, Label: "reject", LabeledAt: now,
					LabeledBy: "pipeline_backfill", Reason: string(a.Status), Source: "pipeline_backfill",
				}
				if err := labels.Label(cmd.Context(), example); err != nil {
					if errors.Is(err, training.ErrAlreadyLabeled) {
						skipped++
						continue
					}
					return fmt.Errorf("train backfill-rejects: label %s: %w", a.ID, err)
				}
				labeled++
			}
			cmd.Printf("backfill-rejects: %d candidate(s) found, %d labeled, %d skipped (already labeled) (dry-run=%v)\n",
				len(candidates), labeled, skipped, dryRun)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20000, "maximum number of articles to backfill in one run")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report candidate counts without writing any labels")
	return cmd
}
