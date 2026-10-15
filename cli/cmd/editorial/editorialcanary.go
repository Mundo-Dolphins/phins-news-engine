package editorial

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// CanaryNow returns the reference time canary status/promote's criteria
// evaluation treats as "now". It is a package-level var — not a direct
// clock.Real{}.Now() call — specifically so tests in editorialcanary_test.go
// (external cmd_test package, like every other test file in this package)
// can pin a fixed reference time: evaluateCanaryCriteria's ElapsedDays is
// computed as now.Sub(earliestEnforced), so pinning "now" keeps that
// arithmetic — and the PASS/FAIL assertions built on it — deterministic
// regardless of how long seeding a fixture and dispatching the command
// actually takes in a given test run. This mirrors the
// NewRemoteSyncClient seam in editorialcycle.go — same rationale, same
// shape. Reset it after overriding it in a test.
var CanaryNow = func() time.Time { return clock.Real{}.Now() }

// CanaryCriteriaResult is canary status's pass/fail evaluation against
// EditorialRolloutConfig's canary_* fields. ArticleCount and ElapsedDays
// are computed from real enforced-arm decisions only (reason codes
// "editorial:canary_reject" and "editorial:canary_ambiguous_reject",
// plus terminal "editorial_veto" rejections),
// counted and dated across the enforced arm's entire recorded history —
// these are the only two gates actually implemented. Precision and
// FalsePositiveRate are left at their zero value: computing them
// requires a labeled ground truth that article_processing_events alone
// doesn't carry (see evaluateCanaryCriteria's doc comment).
type CanaryCriteriaResult struct {
	Passed            bool
	Failures          []string
	ArticleCount      int
	ElapsedDays       int
	Precision         float64
	FalsePositiveRate float64
}

// CanaryCriteriaCoverageNote is printed alongside every status/promote
// verdict so a reader never mistakes an unevaluated criterion for a
// measured one: `editorial_rollout` configures four gates, but only two
// are computable from article_processing_events alone.
const CanaryCriteriaCoverageNote = "(2 of 4 configured criteria evaluated: article count, elapsed days; precision and false-positive-rate require labeled ground truth not yet available)"

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
			result, err := evaluateCanaryCriteria(cmd.Context(), decisions, cfg, CanaryNow())
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
			result, err := evaluateCanaryCriteria(cmd.Context(), decisions, cfg, CanaryNow())
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

// evaluateCanaryCriteria's Precision/FalsePositiveRate fields need a
// labeled ground truth to compute against, which article_processing_events
// alone doesn't carry — they are left at their zero value with this
// comment noting they require cross-referencing human_labels/reconciled
// outcomes, a known limitation documented in the runbook (§ Task 14)
// rather than a TDD gap: only the count/day gates are implemented for
// real here.
//
// The event query below is deliberately NOT bounded by CanaryMinDays:
// ArticleCount/ElapsedDays must reflect the true cumulative history of
// the enforced arm since canary mode actually started, not merely
// whatever falls inside a window whose own width is CanaryMinDays — a
// query bounded that way could never observe more than CanaryMinDays of
// elapsed time in the first place, making the day-gate structurally
// unsatisfiable (a bug caught in review). ListByEventTypesSince's since
// argument is instead the zero time.Time, i.e. "since the beginning of
// recorded history"; CanaryMinDays/CanaryMinArticles are used only in
// the threshold comparisons below.
func evaluateCanaryCriteria(ctx context.Context, decisions appout.DecisionRepository, cfg *config.Config, now time.Time) (CanaryCriteriaResult, error) {
	events, err := decisions.ListByEventTypesSince(ctx, []string{"editorial", "editorial_veto", "editorial_canary_shadow", "editorial_canary_shadow_would_reject"}, time.Time{}, 1000000)
	if err != nil {
		return CanaryCriteriaResult{}, fmt.Errorf("editorial canary: list events: %w", err)
	}
	var enforced int
	var earliestEnforced time.Time
	for _, e := range events {
		if e.Mode != "canary" {
			continue
		}
		// The enforced arm's real decisions are the score/ambiguous
		// rejects plus veto-triggered rejects — the veto gate fires
		// before the score is ever consulted, so omitting it would
		// undercount enforced-arm activity and bias both gates
		// optimistically. A veto event is terminal (and thus
		// enforced-arm) only when it actually moved the article to
		// rejected_quality; appendEditorialVetoEvent's log-only twin
		// leaves ToStatus at the article's current status.
		terminalVeto := e.EventType == "editorial_veto" && e.ToStatus == article.StatusRejectedQuality
		if !terminalVeto && e.ReasonCode != "editorial:canary_reject" && e.ReasonCode != "editorial:canary_ambiguous_reject" {
			continue // shadow-arm event within canary mode (e.g. editorial_canary_shadow*), not part of the enforced-arm sample
		}
		enforced++
		if earliestEnforced.IsZero() || e.OccurredAt.Before(earliestEnforced) {
			earliestEnforced = e.OccurredAt
		}
	}
	result := CanaryCriteriaResult{ArticleCount: enforced}
	if !earliestEnforced.IsZero() {
		result.ElapsedDays = int(now.Sub(earliestEnforced).Hours() / 24)
	}
	var failures []string
	if enforced < cfg.EditorialRollout.CanaryMinArticles {
		failures = append(failures, fmt.Sprintf("article count %d < min %d", enforced, cfg.EditorialRollout.CanaryMinArticles))
	}
	if result.ElapsedDays < cfg.EditorialRollout.CanaryMinDays {
		failures = append(failures, fmt.Sprintf("elapsed days %d < min %d", result.ElapsedDays, cfg.EditorialRollout.CanaryMinDays))
	}
	result.Passed = len(failures) == 0
	result.Failures = failures
	return result, nil
}
