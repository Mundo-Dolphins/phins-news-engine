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
			p, err := repo.Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("entities approve: %w", err)
			}
			if p.Status != entityproposal.StatusPending {
				return fmt.Errorf("entities approve: proposal %s is already %s", p.ID, p.Status)
			}

			resolved := p
			if p.Kind == entityproposal.KindPossibleDuplicate {
				switch {
				case mergeInto != "" && asNew:
					return fmt.Errorf("entities approve: pass only one of --merge-into or --as-new")
				case mergeInto != "":
					resolved.Kind = entityproposal.KindNewAlias
					resolved.TargetEntityID = mergeInto
				case asNew:
					resolved.Kind = entityproposal.KindNewEntity
				default:
					return fmt.Errorf("entities approve: proposal %s is a possible duplicate — pass --merge-into <entity-id> or --as-new", p.ID)
				}
			}

			now := time.Now().UTC()
			paths := entitydiscovery.CatalogPaths{DictionaryPath: dictionaryPath, ManualCatalogPath: manualPath, GeneratedCatalogPath: generatedPath}
			if err := entitydiscovery.ApplyApproval(resolved, paths, now); err != nil {
				return fmt.Errorf("entities approve: %w", err)
			}
			if err := repo.UpdateStatus(cmd.Context(), p.ID, entityproposal.StatusApproved, now); err != nil {
				return fmt.Errorf("entities approve: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "approved %s (%s)\n", p.CanonicalName, p.ID)
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
