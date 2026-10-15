package configcmd

import (
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

func NewCommand() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration utilities",
	}
	configCmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Load and strictly validate the YAML configuration file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			profile, err := config.ResolveProfile(shared.ProfileFlag)
			if err != nil {
				return err
			}
			catalog, err := sourcesfile.Load(cfg.Fetch.SourcesPath)
			if err != nil {
				return fmt.Errorf("load sources: %w", err)
			}
			_, report, err := fetcherwiring.Build(cfg, catalog, profile, clock.Real{}, shared.DiscardLogger{}, shared.FetcherExtensions)
			if err != nil {
				return err
			}

			cmd.Printf("config OK: %s (profile=%s)\n", shared.ConfigPath, profile)
			cmd.Printf("  active sources: %s\n", strings.Join(report.Active(), ", "))
			for category, reason := range report.Inactive() {
				cmd.Printf("  inactive: %s (%s)\n", category, reason)
			}
			cmd.Printf("  sqlite.path=%s\n", cfg.SQLite.Path)
			cmd.Printf("  scheduler.check_interval=%s\n", cfg.Scheduler.CheckInterval())
			cmd.Printf("  scheduler.cleanup_interval=%s\n", cfg.Scheduler.CleanupInterval())
			cmd.Printf("  retention.rejected_articles_days=%d\n", cfg.Retention.RejectedArticlesDays)
			cmd.Printf("  retention.duplicates_days=%d\n", cfg.Retention.DuplicatesDays)
			cmd.Printf("  retention.audit_events_days=%d\n", cfg.Retention.AuditEventsDays)
			cmd.Printf("  retention.publication_attempts_days=%d\n", cfg.Retention.PublicationAttemptsDays)
			cmd.Printf("  retention.clusters_days=%d\n", cfg.Retention.ClustersDays)
			return nil
		},
	})
	return configCmd
}
