package editorial

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// editorialReviewEventTypes are the event_type values ProcessCycle (Task
// 25) writes for editorial decisions: shadow-mode audits, enforce-mode
// ambiguous-zone flags, and enforce-mode rejects.
var editorialReviewEventTypes = []string{"editorial_shadow", "editorial_ambiguous", "editorial"}

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
			events, err := decisions.ListByEventTypesSince(cmd.Context(), editorialReviewEventTypes, since, limit)
			if err != nil {
				return err
			}

			counts := map[string]int{}
			for _, e := range events {
				counts[e.EventType]++
			}
			cmd.Printf("editorial decisions in the last %d days (limit %d): %d total\n", days, limit, len(events))
			for _, et := range editorialReviewEventTypes {
				if counts[et] > 0 {
					cmd.Printf("  %s: %d\n", et, counts[et])
				}
			}

			for _, e := range events {
				title, status := "(article not found)", "(unknown)"
				if a, err := articles.FindByID(cmd.Context(), e.ArticleID); err == nil {
					title, status = a.Title, string(a.Status)
				}
				cmd.Printf("%s  %-20s score=%.1f  actual_status=%-24s  %s\n",
					e.OccurredAt.Format(time.RFC3339), e.EventType, e.Score, status, title)
			}
			return nil
		},
	}
	reviewCmd.Flags().IntVar(&days, "days", 7, "how many days back to report")
	reviewCmd.Flags().IntVar(&limit, "limit", 50, "maximum number of decisions to report")
	return reviewCmd
}
