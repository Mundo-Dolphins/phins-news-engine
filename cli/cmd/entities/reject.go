// cli/cmd/entities/reject.go
package entities

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
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
			p, err := entitydiscovery.Reject(cmd.Context(), repo, args[0], time.Now().UTC())
			if err != nil {
				return fmt.Errorf("entities reject: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rejected %s (%s)\n", p.CanonicalName, p.ID)
			return nil
		},
	}
	return cmd
}
