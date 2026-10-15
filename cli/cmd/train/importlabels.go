package train

import (
	"fmt"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

type importTally struct{ inserted, unchanged, conflicts, superseded int }

func (t *importTally) add(o sqliteadapter.ImportOutcome) {
	switch o {
	case sqliteadapter.ImportInserted:
		t.inserted++
	case sqliteadapter.ImportUnchanged:
		t.unchanged++
	case sqliteadapter.ImportConflict:
		t.conflicts++
	case sqliteadapter.ImportSuperseded:
		t.superseded++
	}
}

func (t importTally) String() string {
	return fmt.Sprintf("%d inserted, %d unchanged, %d superseded, %d conflict(s) left as-is", t.inserted, t.unchanged, t.superseded, t.conflicts)
}

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
				examples, err := sqliteadapter.NewHumanLabelRepository(archive).ListEditorialExamples(cmd.Context())
				closeErr := archive.Close()
				if err != nil {
					return fmt.Errorf("train import-labels: read %s: %w", path, err)
				}
				if closeErr != nil {
					return closeErr
				}
				var tally importTally
				for _, e := range examples {
					outcome, err := repo.ImportEditorialExample(cmd.Context(), e, false)
					if err != nil {
						return err
					}
					tally.add(outcome)
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
				var tally importTally
				for _, ex := range dataset.Examples {
					outcome, err := repo.ImportEditorialExample(cmd.Context(), editorialExampleFrom(ex), override[ex.LabelSource])
					if err != nil {
						return err
					}
					tally.add(outcome)
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

// editorialExampleFrom maps a dataset row back to the durable snapshot
// shape ExportDataset built it from.
func editorialExampleFrom(ex editorialtrain.TrainingExample) training.EditorialExample {
	return training.EditorialExample{
		ArticleID: ex.ArticleID, Label: ex.Label, Reason: ex.Reason, LabeledAt: ex.LabeledAt,
		Revision: ex.Revision, ContentHash: ex.ContentHash, Source: ex.LabelSource,
		Title: ex.Title, Body: ex.Body, SourceID: ex.SourceID, SourceType: ex.SourceType, Domain: ex.Domain,
		ContentType: ex.ContentType, PublishedAt: ex.PublishedAt, FetchedAt: ex.FetchedAt, ClusterID: ex.ClusterID,
	}
}
