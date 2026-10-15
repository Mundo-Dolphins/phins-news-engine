// cli/cmd/entities/list.go
package entities

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
)

func newListCommand() *cobra.Command {
	var typeFilter string
	var statusFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List LLM-discovered entity proposals",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities list: %w", err)
			}
			defer db.Close()

			repo := sqliteentityproposal.NewEntityProposalRepository(db)
			proposals, err := repo.List(cmd.Context(), typeFilter, statusFilter)
			if err != nil {
				return fmt.Errorf("entities list: %w", err)
			}
			sort.SliceStable(proposals, func(i, j int) bool {
				return entityproposal.Score(proposals[i]) > entityproposal.Score(proposals[j])
			})

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tKIND\tNAME\tOCCURRENCES\tCONFIDENCE\tSTATUS")
			for _, p := range proposals {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%.2f\t%s\n", p.ID, p.EntityType, p.Kind, p.CanonicalName, p.Occurrences, p.Confidence, p.Status)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&typeFilter, "type", "", "filter by entity type: player|coach|executive|team")
	cmd.Flags().StringVar(&statusFilter, "status", "pending", "filter by status: pending|approved|rejected (pass \"\" for all)")
	return cmd
}
