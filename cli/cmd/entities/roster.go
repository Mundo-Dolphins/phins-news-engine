// cli/cmd/entities/roster.go
package entities

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse/rostersync"
)

func newRosterCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "roster", Short: "Manage the local nflverse roster used by entity verification"}
	cmd.AddCommand(newRosterSyncCommand())
	return cmd
}

// SyncRoster downloads the nflverse roster atomically and validates it. The
// destination is pathOverride, else rc.Path; the URL is urlOverride, else
// rc.URL, else the nflverse default for the current year. The previous file is
// kept when the download or its validation fails.
func SyncRoster(ctx context.Context, rc config.RosterConfig, urlOverride, pathOverride string) (int, string, error) {
	dest, src := pathOverride, urlOverride
	if dest == "" {
		dest = rc.Path
		if src == "" {
			src = rc.URL
		}
	}
	if dest == "" {
		return 0, "", fmt.Errorf("no roster path (set entity_discovery.roster.path or pass --path)")
	}
	if src == "" {
		src = nflverse.DefaultURL(time.Now().Year())
	}
	var count int
	validate := func(p string) error {
		f, err := os.OpenInRoot(filepath.Dir(p), filepath.Base(p))
		if err != nil {
			return err
		}
		defer f.Close()
		players, err := nflverse.ParseRoster(f)
		if err != nil {
			return err
		}
		if len(players) == 0 {
			return fmt.Errorf("roster file has no players")
		}
		count = len(players)
		return nil
	}
	if err := rostersync.Download(ctx, netguard.NewClient(60*time.Second, 60*time.Second, netguard.AllowPrivate), src, dest, validate); err != nil {
		return 0, "", err
	}
	return count, dest, nil
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
