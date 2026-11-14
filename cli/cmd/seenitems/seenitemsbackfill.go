package seenitems

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/seenitemsbackfill"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// NewCommand returns the seenitemsbackfill command.
func NewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "seenitemsbackfill",
		Short: "Backfill seen_items rows from existing articles history (recommended but not required)",
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			result, err := seenitemsbackfill.Run(ctx, db, time.Now().UTC())
			if err != nil {
				return err
			}
			cmd.Println(result.Summary())
			return nil
		},
	}
}
