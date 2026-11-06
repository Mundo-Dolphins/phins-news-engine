// Package stats reports a health snapshot of recent pipeline activity —
// sources processed, articles analyzed/rejected, Telegram delivery, and
// process-log cycle health — for an operator who just wants to know
// whether the bot is actually working.
package stats

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/pipelinestats"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// NewCommand builds the `stats` command.
func NewCommand() *cobra.Command {
	var since string
	var jsonOut bool
	var logPath string
	var topN int

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report pipeline health over a recent time window: sources, articles, rejections, Telegram delivery, and log activity",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("log-path") {
				logPath = shared.StatsLogPath
			}
			duration, err := time.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("stats: invalid --since %q: %w", since, err)
			}
			now := clock.Real{}.Now()
			sinceTime := now.Add(-duration)

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return fmt.Errorf("stats: open database: %w", err)
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return fmt.Errorf("stats: migrate database: %w", err)
			}

			articles := sqliteadapter.NewArticleRepository(db)
			outbox := sqliteadapter.NewOutboxRepository(db)

			report, err := pipelinestats.Build(cmd.Context(), articles, outbox, sinceTime, now, logPath, topN)
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			printReport(cmd, report)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "how far back to report, as a Go duration (e.g. 12h, 24h)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON instead of text")
	cmd.Flags().StringVar(&logPath, "log-path", shared.DefaultStatsLogPath, "path to the process log to scan for cycle activity (skipped if missing)")
	cmd.Flags().IntVar(&topN, "top-sources", 10, "how many sources to include in the top-sources breakdown")
	return cmd
}

func printReport(cmd *cobra.Command, r pipelinestats.Report) {
	cmd.Printf("since=%s window=%.1fh sources=%d analyzed=%d accepted=%d rejected=%d pending=%d reject_rate=%.3f\n",
		r.Since.Format(time.RFC3339), r.WindowHours, r.SourcesProcessed, r.ArticlesAnalyzed, r.ArticlesAccepted, r.ArticlesRejected, r.ArticlesPending, r.RejectRate)

	if len(r.RejectedByStatus) > 0 {
		cmd.Println("rejected by status:")
		for status, n := range r.RejectedByStatus {
			cmd.Printf("  %-24s %d\n", status, n)
		}
	}
	if len(r.RejectedByReason) > 0 {
		cmd.Println("rejected by reason:")
		for reason, n := range r.RejectedByReason {
			cmd.Printf("  %-40s %d\n", reason, n)
		}
	}
	if len(r.TopSources) > 0 {
		cmd.Println("top sources:")
		for _, s := range r.TopSources {
			cmd.Printf("  %-30s total=%-5d rejected=%d\n", s.Source, s.Total, s.Rejected)
		}
	}

	cmd.Printf("telegram delivered_in_window=%d\n", r.TelegramDeliveredInWindow)
	cmd.Println("telegram backlog (current, all-time):")
	for _, status := range []string{"pending", "leased", "sent_unconfirmed", "retryable", "quarantined", "dead"} {
		cmd.Printf("  %-18s %d\n", status, r.TelegramBacklog[status])
	}

	if r.LogUnavailable != "" {
		cmd.Printf("logs: unavailable at %s (%s)\n", r.LogPath, r.LogUnavailable)
		return
	}
	cmd.Printf("logs (%s): cycles_run=%d fetched_sum=%d error_lines=%d", r.LogPath, r.LogCyclesRun, r.LogFetchedSum, r.LogErrorLines)
	if r.LogLastCycleAt != nil {
		cmd.Printf(" last_cycle_at=%s", r.LogLastCycleAt.Format(time.RFC3339))
	}
	cmd.Println()
}
