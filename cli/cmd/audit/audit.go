package audit

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func NewCommand() *cobra.Command {
	auditCmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit recent pipeline decisions: duplicates and editorial quality",
	}
	auditCmd.AddCommand(newDuplicatesCommand())
	auditCmd.AddCommand(newQualityCommand())
	return auditCmd
}

// newDuplicatesCommand reports articles rejected as duplicates in the
// requested window, alongside the prose reason ProcessCycle recorded for
// each decision. Mirrors v1's audit-duplicates tool (see
// docs/V1-V2-MIGRATION.md row 28), but reads from the existing
// article_processing_events audit trail (ADR 0005) instead of a dedicated
// table.
func newDuplicatesCommand() *cobra.Command {
	var days, limit int
	dupCmd := &cobra.Command{
		Use:   "duplicates",
		Short: "Report recent duplicate-rejection decisions",
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
			events, err := decisions.ListByEventTypesSince(cmd.Context(), []string{"dedup"}, since, limit)
			if err != nil {
				return err
			}

			cmd.Printf("duplicate rejections in the last %d days (limit %d): %d total\n", days, limit, len(events))
			for _, e := range events {
				title := "(article not found)"
				if a, err := articles.FindByID(cmd.Context(), e.ArticleID); err == nil {
					title = a.Title
				}
				cmd.Printf("%s  %-40s  %s\n", e.OccurredAt.Format(time.RFC3339), title, e.ReasonDetail)
			}
			return nil
		},
	}
	dupCmd.Flags().IntVar(&days, "days", 7, "how many days back to report")
	dupCmd.Flags().IntVar(&limit, "limit", 100, "maximum number of decisions to report")
	return dupCmd
}

// newQualityCommand reports editorial decisions alongside their editorial
// score and the threshold that gated them, flagging any whose score fell
// below the quality_threshold_used at accept time — a signal that
// editorial mode was "shadow" (score recorded, not enforced) rather than
// "enforce". Mirrors v1's published_quality_audit tool.
func newQualityCommand() *cobra.Command {
	var days, limit int
	qualCmd := &cobra.Command{
		Use:   "quality",
		Short: "Report editorial decisions' scores against their gating threshold",
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
			events, err := decisions.ListByEventTypesSince(cmd.Context(), []string{"editorial_shadow", "editorial_ambiguous", "editorial"}, since, limit)
			if err != nil {
				return err
			}

			belowThreshold := 0
			cmd.Printf("editorial decisions in the last %d days (limit %d): %d total\n", days, limit, len(events))
			for _, e := range events {
				title, status := "(article not found)", "(unknown)"
				if a, err := articles.FindByID(cmd.Context(), e.ArticleID); err == nil {
					title, status = a.Title, string(a.Status)
				}
				flag := ""
				if e.Score < e.Threshold {
					flag = "  [below threshold]"
					belowThreshold++
				}
				cmd.Printf("%s  score=%.1f threshold=%.1f  status=%-20s  %s%s\n",
					e.OccurredAt.Format(time.RFC3339), e.Score, e.Threshold, status, title, flag)
			}
			cmd.Printf("%d of %d decisions were below their gating threshold\n", belowThreshold, len(events))
			return nil
		},
	}
	qualCmd.Flags().IntVar(&days, "days", 7, "how many days back to report")
	qualCmd.Flags().IntVar(&limit, "limit", 100, "maximum number of decisions to report")
	return qualCmd
}
