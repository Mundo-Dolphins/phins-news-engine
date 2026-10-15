package editorial

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// editorialMetricsEventTypes is every event_type this report aggregates
// over — kept as an explicit allowlist so a future unrelated event_type
// added elsewhere never silently skews these numbers.
var editorialMetricsEventTypes = []string{
	"editorial", "editorial_shadow", "editorial_ambiguous",
	"editorial_canary_shadow", "editorial_canary_shadow_would_reject",
}

// MetricsReport is editorial metrics' output shape, printed as text or
// (--json) as JSON. Score/reason fields only ever carry aggregate
// counts and IDs — never article title/summary text.
//
// There is deliberately no per-source breakdown here: out.Event carries
// no source ID, so a by-source reject rate would require a new join
// against the articles table that this report does not otherwise need.
type MetricsReport struct {
	Since         time.Time      `json:"since"`
	Total         int            `json:"total"`
	ByMode        map[string]int `json:"by_mode"`
	ByReasonCode  map[string]int `json:"by_reason_code"`
	RejectRate    float64        `json:"reject_rate"`
	AmbiguousRate float64        `json:"ambiguous_rate"`
	ByScoreDecile [10]int        `json:"by_score_decile"`
}

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
			report, err := buildMetricsReport(cmd.Context(), decisions, clock.Real{}.Now().Add(-duration))
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

func buildMetricsReport(ctx context.Context, decisions appout.DecisionRepository, since time.Time) (MetricsReport, error) {
	events, err := decisions.ListByEventTypesSince(ctx, editorialMetricsEventTypes, since, 100000)
	if err != nil {
		return MetricsReport{}, fmt.Errorf("editorial metrics: list events: %w", err)
	}
	report := MetricsReport{Since: since, ByMode: map[string]int{}, ByReasonCode: map[string]int{}}
	var rejected, ambiguous int
	for _, e := range events {
		report.Total++
		report.ByMode[e.Mode]++
		if e.ReasonCode != "" {
			report.ByReasonCode[e.ReasonCode]++
		}
		// Terminal rejects (score, ambiguous, canary-enforced-arm) all
		// share EventType "editorial" (see rejectEditorialScored) and
		// are distinguished only by ReasonCode — classify on ReasonCode,
		// never EventType, for these. "editorial_ambiguous" EventType is
		// the distinct non-terminal case: AmbiguousPolicy=accept logged
		// it without rejecting, so it counts toward AmbiguousRate but
		// not RejectRate.
		switch {
		case e.ReasonCode == "editorial:reject_score" || e.ReasonCode == "editorial:canary_reject":
			rejected++
		case e.ReasonCode == "editorial:ambiguous_reject" || e.ReasonCode == "editorial:canary_ambiguous_reject":
			rejected++
			ambiguous++
		case e.EventType == "editorial_ambiguous":
			ambiguous++
		}
		decile := int(e.Score / 10)
		if decile > 9 {
			decile = 9
		}
		if decile >= 0 {
			report.ByScoreDecile[decile]++
		}
	}
	if report.Total > 0 {
		report.RejectRate = float64(rejected) / float64(report.Total)
		report.AmbiguousRate = float64(ambiguous) / float64(report.Total)
	}
	return report, nil
}
