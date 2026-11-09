// cli/cmd/entities/league.go
package entities

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

// printLeagueStats prints the gazetteer and chooser summary lines.
func printLeagueStats(out interface{ Write([]byte) (int, error) }, st entitydiscovery.LeagueStats) {
	fmt.Fprintf(out, "league gazetteer: resolved full-name %d, surname %d, typo %d, correction %d; suggested %d; ambiguous %d; noise %d; not_an_nfl_team %d\n",
		st.ResolvedFullName, st.ResolvedSurname, st.ResolvedTypo, st.ResolvedCorrection, st.Suggested, st.Ambiguous, st.Noise, st.NotNFLTeam)
	fmt.Fprintf(out, "league chooser: calls %d, accepted %d, gated %d, errors %d\n", st.ChooserCalls, st.ChooserAccepted, st.ChooserGated, st.ChooserErrors)
}

func newLeagueCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "league", Short: "League gazetteer tools"}
	cmd.AddCommand(newLeagueEvaluateCommand())
	return cmd
}

func newLeagueEvaluateCommand() *cobra.Command {
	var manualPath, generatedPath string
	cmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Read-only quality report of the league gazetteer on the pending backlog (never writes)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, cfg, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities league evaluate: %w", err)
			}
			defer db.Close()
			rep, err := entitywiring.EvaluateLeague(cmd.Context(), db, cfg, manualPath, generatedPath, time.Now(), cliLoggers(cmd))
			if err != nil {
				return fmt.Errorf("entities league evaluate: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), rep.Text())
			return nil
		},
	}
	cmd.Flags().StringVar(&manualPath, "manual", "configs/entity_catalog_manual.yml", "path to the manual entity catalog YAML file")
	cmd.Flags().StringVar(&generatedPath, "generated", "configs/entity_catalog_generated.yml", "path to the generated entity catalog YAML file")
	return cmd
}
