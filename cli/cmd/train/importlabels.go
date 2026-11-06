package train

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/humanlabels"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newImportLabelsCommand restores label snapshots into the operational
// database's editorial_examples: from old database backups (labels a sync
// without merge dropped) and from a reviewed dataset file (decisions made
// by `train review-dataset`, which never wrote them to SQLite). Imports
// are additive; only examples whose label source is listed in
// --override-label-source may supersede a disagreeing active label.
func newImportLabelsCommand() *cobra.Command {
	var fromDBs, fromDatasets, overrideSources []string
	cmd := &cobra.Command{
		Use:   "import-labels",
		Short: "Restore label snapshots from old database backups or a reviewed dataset file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() { fromDBs, fromDatasets, overrideSources = nil, nil, nil }()
			if len(fromDBs) == 0 && len(fromDatasets) == 0 {
				return fmt.Errorf("train import-labels: give at least one --from-db or --from-dataset")
			}
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			repo := sqliteadapter.NewHumanLabelRepository(db)

			for _, path := range fromDBs {
				archive, err := infrasqlite.OpenReadOnly(path, cfg.SQLite.BusyTimeout())
				if err != nil {
					return err
				}
				tally, err := humanlabels.ImportFromDB(cmd.Context(), repo, sqliteadapter.NewHumanLabelRepository(archive))
				closeErr := archive.Close()
				var readErr *humanlabels.ArchiveReadError
				if errors.As(err, &readErr) {
					return fmt.Errorf("train import-labels: read %s: %w", path, err)
				}
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
				cmd.Printf("%s: %s\n", path, tally)
			}

			override := make(map[string]bool, len(overrideSources))
			for _, s := range overrideSources {
				override[s] = true
			}
			for _, path := range fromDatasets {
				dataset, err := editorialtrain.LoadDataset(path)
				if err != nil {
					return err
				}
				tally, err := humanlabels.ImportFromDataset(cmd.Context(), repo, dataset, override)
				if err != nil {
					return err
				}
				cmd.Printf("%s: %s\n", path, tally)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&fromDBs, "from-db", nil, "old SQLite snapshot whose active labels to import, read-only (repeatable); never overrides an existing label")
	cmd.Flags().StringSliceVar(&fromDatasets, "from-dataset", nil, "dataset YAML whose labels to import (repeatable)")
	cmd.Flags().StringSliceVar(&overrideSources, "override-label-source", nil, "label_source values (e.g. interactive_review) allowed to supersede a disagreeing active label (repeatable)")
	return cmd
}
