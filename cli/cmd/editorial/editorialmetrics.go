package editorial

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialmetrics"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

// MetricsReport is editorial metrics' output shape (see
// editorialmetrics.MetricsReport).
type MetricsReport = editorialmetrics.MetricsReport

func newMetricsCommand() *cobra.Command {
	var since string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Report editorial-classifier decision volume, zone rates, and score distribution over a time window",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// root command is a package-level singleton re-Execute()'d across tests and
			// pflag never resets a flag to its default when a later invocation omits
			// it, so restore the defaults when this run ends.
			defer func() { since, jsonOut = "24h", false }()
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			duration, err := time.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("editorial metrics: invalid --since %q: %w", since, err)
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return fmt.Errorf("editorial metrics: open database: %w", err)
			}
			defer db.Close()

			decisions := sqliteadapter.NewDecisionRepository(db)
			report, err := editorialmetrics.Metrics(cmd.Context(), decisions, clock.Real{}.Now().Add(-duration))
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			cmd.Printf("since=%s total=%d reject_rate=%.3f ambiguous_rate=%.3f\n", report.Since.Format(time.RFC3339), report.Total, report.RejectRate, report.AmbiguousRate)
			for mode, n := range report.ByMode {
				cmd.Printf("  mode=%s count=%d\n", mode, n)
			}
			for code, n := range report.ByReasonCode {
				cmd.Printf("  reason_code=%s count=%d\n", code, n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "how far back to aggregate, as a Go duration (e.g. 24h, 168h)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON instead of text")
	return cmd
}
