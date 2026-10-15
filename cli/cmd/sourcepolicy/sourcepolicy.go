package sourcepolicy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func NewCommand() *cobra.Command {
	sourcePolicyCmd := &cobra.Command{
		Use:   "source-policy",
		Short: "Inspect, validate, audit, and simulate per-source deduplication/publication policies",
	}
	sourcePolicyCmd.AddCommand(newValidateCommand())
	sourcePolicyCmd.AddCommand(newInspectCommand())
	sourcePolicyCmd.AddCommand(newAuditCommand())
	sourcePolicyCmd.AddCommand(newSimulateCommand())
	return sourcePolicyCmd
}

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configs/source-policies.yml (schema + orphaned policies)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}

			sources, err := sourcesfile.Load(cfg.Fetch.SourcesPath)
			if err != nil {
				return err
			}
			policies, err := sourcepolicyfile.Load(cfg.SourcePolicy.Path)
			if err != nil {
				return err
			}

			result := usecase.ValidateSourcePolicies(sources, policies)
			if len(result.Issues) == 0 {
				cmd.Println("source policies: OK")
				return nil
			}
			for _, issue := range result.Issues {
				cmd.Println("ISSUE:", issue)
			}
			return fmt.Errorf("source-policy validate: %d issue(s) found", len(result.Issues))
		},
	}
}

func newInspectCommand() *cobra.Command {
	var sourceID string
	c := &cobra.Command{
		Use:   "inspect",
		Short: "Print the effective resolved policy for one source",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			file, err := sourcepolicyfile.Load(cfg.SourcePolicy.Path)
			if err != nil {
				return err
			}
			resolver := sourcepolicyfile.NewResolver(file)
			policy := resolver.Resolve(sourceID)
			cmd.Printf("source_id: %s\n", policy.SourceID)
			cmd.Printf("authority: %s\n", policy.Authority)
			cmd.Printf("mode: %s\n", resolver.Mode())
			cmd.Printf("retain_near_duplicates: %v\n", policy.RetainNearDuplicates)
			cmd.Printf("representative_preference: %s\n", policy.RepresentativePreference)
			cmd.Printf("allow_official_confirmation: %v\n", policy.AllowOfficialConfirmation)
			cmd.Printf("allow_substantial_update: %v\n", policy.AllowSubstantialUpdate)
			cmd.Printf("publish_confirmation_without_new_information: %v\n", policy.PublishConfirmationWithoutNewInformation)
			cmd.Printf("minimum_update_novelty: %v\n", policy.MinimumUpdateNovelty)
			cmd.Printf("event_cooldown: %v\n", policy.EventCooldown)
			return nil
		},
	}
	c.Flags().StringVar(&sourceID, "source", "", "source_id to inspect (required)")
	_ = c.MarkFlagRequired("source")
	return c
}

func newAuditCommand() *cobra.Command {
	var from, to, sourceID, mode, format string
	c := &cobra.Command{
		Use:   "audit",
		Short: "Summarize source-policy decisions recorded over a date range",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			fromT, err := time.Parse("2006-01-02", from)
			if err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			toT, err := time.Parse("2006-01-02", to)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()

			ctx := cmd.Context()
			if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
				return err
			}

			repo := sqliteadapter.NewSourcePolicyRepository(db)
			report, err := usecase.AuditSourcePolicies(ctx, repo, fromT, toT, sourceID, mode)
			if err != nil {
				return err
			}

			switch format {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			case "text":
				cmd.Printf("total decisions: %d\n", report.TotalDecisions)
				cmd.Printf("divergences from standard policy: %d\n", report.Divergences)
				for code, n := range report.ByDecisionCode {
					cmd.Printf("  %s: %d\n", code, n)
				}
				cmd.Printf("by cooldown outcome:\n")
				merged := make(map[string]int, len(report.ByCooldownOutcome))
				for outcome, n := range report.ByCooldownOutcome {
					label := outcome
					if label == "" {
						label = "not_applicable"
					}
					merged[label] += n
				}
				for label, n := range merged {
					cmd.Printf("  %s: %d\n", label, n)
				}
				return nil
			default:
				return fmt.Errorf("source-policy audit: unknown --format %q (want text or json)", format)
			}
		},
	}
	c.Flags().StringVar(&from, "from", "", "start date, YYYY-MM-DD (required)")
	c.Flags().StringVar(&to, "to", "", "end date, YYYY-MM-DD (required)")
	c.Flags().StringVar(&sourceID, "source", "", "filter by source_id")
	c.Flags().StringVar(&mode, "mode", "", "filter by mode (disabled|shadow|enforce)")
	c.Flags().StringVar(&format, "format", "text", "output format: text|json")
	_ = c.MarkFlagRequired("from")
	_ = c.MarkFlagRequired("to")
	return c
}

func newSimulateCommand() *cobra.Command {
	var articleID string
	c := &cobra.Command{
		Use:   "simulate",
		Short: "Show every recorded source-policy decision for one article",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()

			ctx := cmd.Context()
			if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
				return err
			}

			sim, err := usecase.SimulateSourcePolicy(ctx, sqliteadapter.NewSourcePolicyRepository(db), sqliteadapter.NewArticleRepository(db), articleID)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "article: %s (source: %s)\n", sim.ArticleID, sim.SourceID)
			if len(sim.Decisions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no source-policy decisions recorded for this article")
				return nil
			}
			for _, d := range sim.Decisions {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s -> code=%s should_publish_standard=%v should_publish_special=%v mode=%s\n  %s\n",
					d.DecidedAt.Format(time.RFC3339), d.DedupMatchType, d.DecisionCode, d.WouldPublishStandard, d.WouldPublishSpecial, d.Mode, d.Explanation)
			}
			return nil
		},
	}
	c.Flags().StringVar(&articleID, "article-id", "", "article ID to simulate (required)")
	_ = c.MarkFlagRequired("article-id")
	return c
}
