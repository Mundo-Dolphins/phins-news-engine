package editorial

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialmetrics"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// NewReviewCommand builds `editorial-review`, which reports recent
// editorial-classifier decisions against each article's actual outcome.
func NewReviewCommand() *cobra.Command {
	var days, limit int
	reviewCmd := &cobra.Command{
		Use:   "editorial-review",
		Short: "Report recent editorial-classifier decisions against each article's actual outcome",
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

			decisions := sqliteadapter.NewDecisionRepository(db)
			articles := sqliteadapter.NewArticleRepository(db)

			since := time.Now().UTC().AddDate(0, 0, -days)
			report, err := editorialmetrics.Review(cmd.Context(), decisions, articles, since, limit)
			if err != nil {
				return err
			}

			cmd.Printf("editorial decisions in the last %d days (limit %d): %d total\n", days, limit, len(report.Rows))
			for _, et := range editorialmetrics.ReviewEventTypes {
				if report.Counts[et] > 0 {
					cmd.Printf("  %s: %d\n", et, report.Counts[et])
				}
			}

			for _, r := range report.Rows {
				cmd.Printf("%s  %-20s score=%.1f  actual_status=%-24s  %s\n",
					r.OccurredAt.Format(time.RFC3339), r.EventType, r.Score, r.ActualStatus, r.Title)
			}
			return nil
		},
	}
	reviewCmd.Flags().IntVar(&days, "days", 7, "how many days back to report")
	reviewCmd.Flags().IntVar(&limit, "limit", 50, "maximum number of decisions to report")
	return reviewCmd
}
