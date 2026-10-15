// Package stats reports a health snapshot of recent pipeline activity —
// sources processed, articles analyzed/rejected, Telegram delivery, and
// process-log cycle health — for an operator who just wants to know
// whether the bot is actually working.
package stats

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// maxArticlesScanned bounds ListSince the same way editorial metrics and
// backtestcandidate do: a health report never needs to read the whole
// table, only a generous ceiling on one window's worth of traffic.
const maxArticlesScanned = 200000

// SourceStat is one source's activity within the report window.
type SourceStat struct {
	Source   string `json:"source"`
	Total    int    `json:"total"`
	Rejected int    `json:"rejected"`
}

// Report is stats' output shape, printed as text or (--json) as JSON.
type Report struct {
	Since       time.Time `json:"since"`
	WindowHours float64   `json:"window_hours"`

	SourcesProcessed int `json:"sources_processed"`
	ArticlesAnalyzed int `json:"articles_analyzed"`
	ArticlesAccepted int `json:"articles_accepted"`
	ArticlesRejected int `json:"articles_rejected"`
	ArticlesPending  int `json:"articles_pending"`

	RejectRate       float64        `json:"reject_rate"`
	RejectedByStatus map[string]int `json:"rejected_by_status"`
	RejectedByReason map[string]int `json:"rejected_by_reason"`

	TopSources []SourceStat `json:"top_sources"`

	TelegramDeliveredInWindow int            `json:"telegram_delivered_in_window"`
	TelegramBacklog           map[string]int `json:"telegram_backlog"`

	LogPath        string     `json:"log_path"`
	LogUnavailable string     `json:"log_unavailable,omitempty"`
	LogCyclesRun   int        `json:"log_cycles_run"`
	LogLastCycleAt *time.Time `json:"log_last_cycle_at,omitempty"`
	LogFetchedSum  int        `json:"log_fetched_sum"`
	LogErrorLines  int        `json:"log_error_lines"`
}

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

			report, err := buildReport(cmd.Context(), articles, outbox, sinceTime, now, logPath, topN)
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

func buildReport(ctx context.Context, articles *sqliteadapter.ArticleRepository, outbox *sqliteadapter.OutboxRepository, since, now time.Time, logPath string, topN int) (Report, error) {
	report := Report{
		Since:            since,
		WindowHours:      now.Sub(since).Hours(),
		RejectedByStatus: map[string]int{},
		RejectedByReason: map[string]int{},
		TelegramBacklog:  map[string]int{},
		LogPath:          logPath,
	}

	list, err := articles.ListSince(ctx, since, maxArticlesScanned)
	if err != nil {
		return Report{}, fmt.Errorf("stats: list articles since %s: %w", since, err)
	}
	report.ArticlesAnalyzed = len(list)

	sources := map[string]bool{}
	bySource := map[string]*SourceStat{}
	for _, a := range list {
		name := a.Source.Name
		if name == "" {
			name = a.Domain
		}
		if name == "" {
			name = "(unknown)"
		}
		sources[name] = true
		stat, ok := bySource[name]
		if !ok {
			stat = &SourceStat{Source: name}
			bySource[name] = stat
		}
		stat.Total++

		switch {
		case strings.HasPrefix(string(a.Status), "rejected_"):
			report.ArticlesRejected++
			stat.Rejected++
			report.RejectedByStatus[string(a.Status)]++
			reason := a.StatusReason
			if reason == "" {
				reason = string(a.Status)
			}
			report.RejectedByReason[reason]++
		case a.Status == article.StatusPublished:
			report.ArticlesAccepted++
		default:
			report.ArticlesPending++
		}
	}
	report.SourcesProcessed = len(sources)
	if report.ArticlesAnalyzed > 0 {
		report.RejectRate = float64(report.ArticlesRejected) / float64(report.ArticlesAnalyzed)
	}

	report.TopSources = topSources(bySource, topN)

	delivered, err := outbox.CountSucceededSince(ctx, since)
	if err != nil {
		return Report{}, fmt.Errorf("stats: count telegram deliveries since %s: %w", since, err)
	}
	report.TelegramDeliveredInWindow = delivered

	statusCounts, err := outbox.StatusCounts(ctx, now)
	if err != nil {
		return Report{}, fmt.Errorf("stats: outbox status counts: %w", err)
	}
	for _, status := range []publishing.OutboxStatus{
		publishing.OutboxPending, publishing.OutboxLeased, publishing.OutboxSentUnconfirmed,
		publishing.OutboxRetryable, publishing.OutboxQuarantined, publishing.OutboxDead,
	} {
		report.TelegramBacklog[string(status)] = statusCounts[status].Count
	}

	scanLogs(logPath, since, &report)

	return report, nil
}

func topSources(bySource map[string]*SourceStat, topN int) []SourceStat {
	stats := make([]SourceStat, 0, len(bySource))
	for _, s := range bySource {
		stats = append(stats, *s)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Total != stats[j].Total {
			return stats[i].Total > stats[j].Total
		}
		return stats[i].Source < stats[j].Source
	})
	if topN > 0 && len(stats) > topN {
		stats = stats[:topN]
	}
	return stats
}

// scanLogs tails logPath for JSON lines with a "time" field at or after
// since, counting process cycles ("process cycle completed"), their
// summed "fetched" field, error-level lines, and the most recent
// timestamp seen — a cheap freshness signal independent of the database,
// since a crashed or wedged process can leave the DB looking healthy
// while nothing has run in hours. A missing or unreadable log file is
// reported, not fatal: this data source is optional (e.g. running
// against a fresh checkout with no synced Pi logs).
func scanLogs(logPath string, since time.Time, report *Report) {
	dir := filepath.Dir(logPath)
	root, err := os.OpenRoot(dir)
	if err != nil {
		report.LogUnavailable = err.Error()
		return
	}
	defer root.Close()

	f, err := root.Open(filepath.Base(logPath))
	if err != nil {
		report.LogUnavailable = err.Error()
		return
	}
	defer f.Close()

	var lastCycleAt time.Time
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		ts, ok := line["time"].(string)
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil || t.Before(since) {
			continue
		}
		if level, _ := line["level"].(string); level == "error" {
			report.LogErrorLines++
		}
		if msg, _ := line["message"].(string); msg == "process cycle completed" {
			report.LogCyclesRun++
			if t.After(lastCycleAt) {
				lastCycleAt = t
			}
			if fetched, ok := line["fetched"].(float64); ok {
				report.LogFetchedSum += int(fetched)
			}
		}
	}
	if !lastCycleAt.IsZero() {
		report.LogLastCycleAt = &lastCycleAt
	}
}

func printReport(cmd *cobra.Command, r Report) {
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
