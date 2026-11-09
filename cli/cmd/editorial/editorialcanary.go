package editorial

import (
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

// CanaryNow returns the reference time canary status/promote's criteria
// evaluation treats as "now". It is a package-level var — not a direct
// clock.Real{}.Now() call — specifically so tests in editorialcanary_test.go
// (external cmd_test package, like every other test file in this package)
// can pin a fixed reference time: editorialmetrics.EvaluateCanary's ElapsedDays is
// computed as now.Sub(earliestEnforced), so pinning "now" keeps that
// arithmetic — and the PASS/FAIL assertions built on it — deterministic
// regardless of how long seeding a fixture and dispatching the command
// actually takes in a given test run. This mirrors the
// NewRemoteSyncClient seam in editorialcycle.go — same rationale, same
// shape. Reset it after overriding it in a test.
var CanaryNow = func() time.Time { return clock.Real{}.Now() }

// CanaryCriteriaResult is canary status's pass/fail evaluation (see
// editorialmetrics.CanaryResult).
type CanaryCriteriaResult = editorialmetrics.CanaryResult

// CanaryCriteriaCoverageNote is printed alongside every status/promote
// verdict (see editorialmetrics.CanaryCoverageNote).
const CanaryCriteriaCoverageNote = editorialmetrics.CanaryCoverageNote

func newCanaryCommand() *cobra.Command {
	canaryCmd := &cobra.Command{Use: "canary", Short: "Inspect and control the editorial classifier's canary rollout"}
	canaryCmd.AddCommand(newCanaryStatusCommand())
	canaryCmd.AddCommand(newCanaryPromoteCommand())
	canaryCmd.AddCommand(newCanaryAbortCommand())
	return canaryCmd
}

func newCanaryStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Evaluate real canary-arm data against editorial_rollout's configured criteria",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return fmt.Errorf("editorial canary status: open database: %w", err)
			}
			defer db.Close()
			decisions := sqliteadapter.NewDecisionRepository(db)
			result, err := editorialmetrics.EvaluateCanary(cmd.Context(), decisions, cfg.EditorialRollout, CanaryNow())
			if err != nil {
				return err
			}
			verdict := "PASS"
			if !result.Passed {
				verdict = "FAIL"
			}
			cmd.Printf("%s: articles=%d elapsed_days=%d precision=n/a (not evaluated) false_positive_rate=n/a (not evaluated)\n",
				verdict, result.ArticleCount, result.ElapsedDays)
			cmd.Printf("  %s\n", CanaryCriteriaCoverageNote)
			for _, f := range result.Failures {
				cmd.Printf("  - %s\n", f)
			}
			return nil
		},
	}
}

func newCanaryPromoteCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Approve promotion to enforce (prints the config change to make; does not edit or deploy) — requires passing canary criteria unless --force",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The root command is a singleton re-Execute()'d across tests; pflag
			// keeps --force from an earlier run, so restore the default afterwards.
			defer func() { force = false }()
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return fmt.Errorf("editorial canary promote: open database: %w", err)
			}
			defer db.Close()
			decisions := sqliteadapter.NewDecisionRepository(db)
			result, err := editorialmetrics.EvaluateCanary(cmd.Context(), decisions, cfg.EditorialRollout, CanaryNow())
			if err != nil {
				return err
			}
			if !result.Passed && !force {
				return fmt.Errorf("editorial canary promote: criteria failing (%v); re-run with --force to override", result.Failures)
			}
			cmd.Printf("promotion approved (forced=%v) — update editorial.mode: enforce in %s and deploy via `deploy run` / `editorial cycle`\n", force, shared.ConfigPath)
			cmd.Printf("  articles=%d elapsed_days=%d precision=n/a (not evaluated) false_positive_rate=n/a (not evaluated)\n", result.ArticleCount, result.ElapsedDays)
			cmd.Printf("  %s\n", CanaryCriteriaCoverageNote)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "promote even if canary criteria are failing (loudly logged)")
	return cmd
}

func newCanaryAbortCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "abort",
		Short: "Print the steps to return editorial.mode to shadow (does not edit or deploy) — pair with train rollback if the promoted model itself is suspect",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Printf("update editorial.mode: shadow in %s and deploy; run `%s train rollback` if the promoted model artifact itself needs reverting\n", shared.ConfigPath, shared.ProgramName)
			return nil
		},
	}
}
