// cli/cmd/entities/show.go
package entities

import (
	"fmt"

	"github.com/spf13/cobra"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
)

func newShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one entity proposal's full detail and evidence",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities show: %w", err)
			}
			defer db.Close()

			repo := sqliteentityproposal.NewEntityProposalRepository(db)
			p, err := repo.Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("entities show: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Name: %s\nType: %s\nKind: %s\n", p.CanonicalName, p.EntityType, p.Kind)
			if p.TargetEntityID != "" {
				fmt.Fprintf(out, "Target entity: %s\n", p.TargetEntityID)
			}
			fmt.Fprintln(out, "\nObserved aliases:")
			if len(p.Aliases) == 0 {
				fmt.Fprintln(out, "(none)")
			}
			for _, a := range p.Aliases {
				fmt.Fprintf(out, "- %s\n", a)
			}
			if pos := p.Metadata["position"]; pos != "" {
				fmt.Fprintf(out, "\nPosition:\n%s\n", pos)
			}
			if affiliationLine(p) != "" || p.Metadata["role"] != "" || p.Metadata["team"] != "" || p.Metadata["league"] != "" || len(p.WebSources) > 0 ||
				p.Metadata["suggested_entity"] != "" || p.Metadata["suggested_candidates"] != "" {
				fmt.Fprintln(out)
				printVerification(out, p, false)
			}
			fmt.Fprintf(out, "\nOccurrences: %d\nConfidence: %.2f\nStatus: %s\nFirst seen: %s\nLast seen: %s\n",
				p.Occurrences, p.Confidence, p.Status, p.FirstSeen.Format("2006-01-02"), p.LastSeen.Format("2006-01-02"))
			if p.DecisionReason != "" {
				fmt.Fprintf(out, "Decision reason: %s\n", p.DecisionReason)
			}
			fmt.Fprintln(out, "\nExample news:")
			for i, s := range p.Sources {
				if i >= 5 {
					break
				}
				fmt.Fprintf(out, "- %s\n", s.ArticleTitle)
			}
			return nil
		},
	}
	return cmd
}
