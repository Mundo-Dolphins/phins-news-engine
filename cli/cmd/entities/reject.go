// cli/cmd/entities/reject.go
package entities

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func newRejectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reject <id>",
		Short: "Reject a pending entity proposal (no catalog file is touched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities reject: %w", err)
			}
			defer db.Close()

			repo := sqliteentityproposal.NewEntityProposalRepository(db)
			p, err := repo.Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("entities reject: %w", err)
			}
			if p.Status != entityproposal.StatusPending {
				return fmt.Errorf("entities reject: proposal %s is already %s", p.ID, p.Status)
			}
			if err := repo.UpdateStatus(cmd.Context(), p.ID, entityproposal.StatusRejected, time.Now().UTC()); err != nil {
				return fmt.Errorf("entities reject: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rejected %s (%s)\n", p.CanonicalName, p.ID)
			return nil
		},
	}
	return cmd
}
