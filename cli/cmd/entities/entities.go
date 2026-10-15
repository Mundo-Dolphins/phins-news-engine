// Package entities provides the `entities` CLI command group for
// entity-catalog maintenance.
package entities

import (
	"database/sql"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycatalogbuild"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func NewCommand() *cobra.Command {
	entitiesCmd := &cobra.Command{Use: "entities", Short: "Entity catalog maintenance"}
	entitiesCmd.AddCommand(newGenerateCommand())
	entitiesCmd.AddCommand(newDiscoverCommand())
	entitiesCmd.AddCommand(newListCommand())
	entitiesCmd.AddCommand(newShowCommand())
	entitiesCmd.AddCommand(newApproveCommand())
	entitiesCmd.AddCommand(newRejectCommand())
	entitiesCmd.AddCommand(newReviewCommand())
	entitiesCmd.AddCommand(newProposalsCommand())
	entitiesCmd.AddCommand(newRosterCommand())
	entitiesCmd.AddCommand(newLeagueCommand())
	entitiesCmd.AddCommand(newWikidataCommand())
	entitiesCmd.AddCommand(newClassifierDictionaryCommand())
	entitiesCmd.AddCommand(newFallbackTestCommand())
	entitiesCmd.AddCommand(newExtractCommand())
	return entitiesCmd
}

// openDatabase loads configs/settings.yml (or --config's path), opens
// and migrates the operational SQLite database, and returns both — the
// shared setup every entities subcommand that touches entity_proposals
// needs. Callers must db.Close() when done.
func openDatabase(cmd *cobra.Command) (*sql.DB, *config.Config, error) {
	cfg, err := config.Load(shared.ConfigPath)
	if err != nil {
		return nil, nil, err
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		return nil, nil, err
	}
	if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, cfg, nil
}

// GenerateCatalog rebuilds the generated half of the entity catalog from the
// team dictionary and returns how many entities it wrote.
func GenerateCatalog(dictionaryPath, out string) (int, error) {
	d, err := dictionaryfile.Load(dictionaryPath)
	if err != nil {
		return 0, err
	}
	for _, issue := range dictionary.Validate(d) {
		if issue.Severity == dictionary.SeverityError {
			return 0, fmt.Errorf("%s failed validation: %s", dictionaryPath, issue)
		}
	}
	entitiesList, err := entitycatalogbuild.Build(d)
	if err != nil {
		return 0, err
	}
	if err := entitycatalog.WriteGenerated(out, entitiesList); err != nil {
		return 0, err
	}
	return len(entitiesList), nil
}

func newGenerateCommand() *cobra.Command {
	var dictionaryPath string
	var out string

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate the player/coach/executive half of the entity catalog from the dictionary",
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := GenerateCatalog(dictionaryPath, out)
			if err != nil {
				return fmt.Errorf("entities generate: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %d entities to %s\n", n, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&dictionaryPath, "dictionary", shared.DefaultDictionaryPath, "path to the team dictionary YAML file")
	cmd.Flags().StringVar(&out, "out", "configs/entity_catalog_generated.yml", "output path for the generated entity catalog")
	return cmd
}
