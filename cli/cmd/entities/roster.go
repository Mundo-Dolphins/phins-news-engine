// cli/cmd/entities/roster.go
package entities

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

func newRosterCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "roster", Short: "Manage the local nflverse roster used by entity verification"}
	cmd.AddCommand(newRosterSyncCommand())
	return cmd
}

// SyncRoster downloads the nflverse roster; see entitywiring.SyncRoster.
func SyncRoster(ctx context.Context, rc config.RosterConfig, urlOverride, pathOverride string) (int, string, error) {
	return entitywiring.SyncRoster(ctx, rc, urlOverride, pathOverride, time.Now())
}

func newRosterSyncCommand() *cobra.Command {
	var url, path string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Download the current nflverse roster CSV (atomic, validated)",
		Long: "Downloads the roster to entity_discovery.roster.path, or to --path when given. " +
			"With --path the configuration file is not read, so the command also works on hosts whose .env lacks the tool secrets.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rc config.RosterConfig
			if path == "" {
				cfg, err := config.Load(shared.ConfigPath)
				if err != nil {
					return fmt.Errorf("entities roster sync: %w", err)
				}
				rc = cfg.EntityDiscovery.Roster
				if rc.Path == "" {
					return fmt.Errorf("entities roster sync: entity_discovery.roster.path is empty (or pass --path)")
				}
			}
			count, dest, err := SyncRoster(cmd.Context(), rc, url, path)
			if err != nil {
				return fmt.Errorf("entities roster sync: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "roster: %d players written to %s\n", count, dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "override the roster CSV URL for this run")
	cmd.Flags().StringVar(&path, "path", "", "write the roster here and skip reading the configuration file")
	return cmd
}
