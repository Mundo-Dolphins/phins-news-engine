// cli/cmd/entities/approve.go
package entities

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func newApproveCommand() *cobra.Command {
	var mergeInto string
	var asNew bool
	var dictionaryPath, manualPath, generatedPath string

	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a pending entity proposal and write it into the deterministic catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities approve: %w", err)
			}
			defer db.Close()

			repo := sqliteentityproposal.NewEntityProposalRepository(db)
			paths := entitydiscovery.CatalogPaths{DictionaryPath: dictionaryPath, ManualCatalogPath: manualPath, GeneratedCatalogPath: generatedPath}
			resolved, err := entitydiscovery.Approve(cmd.Context(), repo, args[0], entitydiscovery.Resolution{MergeInto: mergeInto, AsNew: asNew}, paths, time.Now().UTC())
			if err != nil {
				return fmt.Errorf("entities approve: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "approved %s (%s)\n", resolved.CanonicalName, resolved.ID)
			if resolved.EntityType != entityproposal.EntityTypeTeam {
				fmt.Fprintln(cmd.OutOrStdout(), "remember to run `entities generate` to regenerate the deterministic entity catalog")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&mergeInto, "merge-into", "", "for a possible_duplicate proposal: treat it as a new alias of this existing catalog entity id")
	cmd.Flags().BoolVar(&asNew, "as-new", false, "for a possible_duplicate proposal: treat it as a brand new entity anyway")
	cmd.Flags().StringVar(&dictionaryPath, "dictionary", shared.DefaultDictionaryPath, "path to the team dictionary YAML file")
	cmd.Flags().StringVar(&manualPath, "manual", "configs/entity_catalog_manual.yml", "path to the manual entity catalog YAML file")
	cmd.Flags().StringVar(&generatedPath, "generated", "configs/entity_catalog_generated.yml", "path to the generated entity catalog YAML file (read-only duplicate check; a missing file is ignored)")
	return cmd
}
