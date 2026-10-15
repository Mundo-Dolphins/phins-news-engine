package train

import (
	"fmt"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newRollbackCommand builds `train rollback`, moved from the top-level
// `rollback-model` (#253 part 22) — same underlying modelpromotion.Rollback.
func newRollbackCommand() *cobra.Command {
	rollbackCmd := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back the promoted editorial-classifier model to the previous slot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if cfg.Model.Path == "" {
				return fmt.Errorf("train rollback: model.path is not configured")
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			uow := sqliteadapter.NewUnitOfWork(db)
			registry := sqliteadapter.NewModelRegistryRepository(db)

			if err := modelpromotion.Rollback(cmd.Context(), modelpromotion.Deps{Registry: registry, UOW: uow}, cfg.Model.Path); err != nil {
				return fmt.Errorf("train rollback: %w", err)
			}
			cmd.Printf("rolled back %s to the previous promoted model\n", cfg.Model.Path)
			return nil
		},
	}
	return rollbackCmd
}
