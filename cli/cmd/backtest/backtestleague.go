package backtest

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/evalwiring"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func newLeagueCommand() *cobra.Command {
	var days int
	var dbPath, rosterPath, rulesPath string
	var frequencyPct float64

	cmd := &cobra.Command{
		Use:   "league [flags]",
		Short: "Prove that league entities change nothing but the entity mentions on stored articles",
		Long: `Extracts the last --days days of stored articles twice, without and with the
league-wide player rules built from the nflverse roster, and compares the
results. Every Representation field except Entities, EntityMentions and
UnknownEntities must be identical; the command exits non-zero otherwise.
It also lists league entities that match in more than --frequency-pct percent
of the articles (candidates for common_words in configs/league_alias_rules.yml),
how many unknown entities disappear, and the extraction time per article.

The database is opened read-only; nothing is written. Roster staleness is
ignored here (a backtest on an old roster is still a valid invariant proof).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return fmt.Errorf("backtest league: %w", err)
			}
			db, err := infrasqlite.OpenReadOnly(dbPath, 5*time.Second)
			if err != nil {
				return fmt.Errorf("backtest league: open %s read-only: %w", dbPath, err)
			}
			defer db.Close()

			report, err := evalwiring.League(cmd.Context(), db, cfg, evalwiring.LeagueOptions{
				Days: days, RosterPath: rosterPath, RulesPath: rulesPath, FrequencyPct: frequencyPct,
			}, time.Now())
			if err != nil {
				return fmt.Errorf("backtest league: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), report.Text())
			if !report.OK() {
				return fmt.Errorf("backtest league: invariant violated for %d articles", len(report.InvariantViolations))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 14, "number of days of stored articles to replay")
	cmd.Flags().StringVar(&dbPath, "db", "data/operational.db", "path to the operational.db (opened read-only; use a copy)")
	cmd.Flags().StringVar(&rosterPath, "roster", "", "nflverse roster CSV (default clustering.entity_catalog.league.roster_path)")
	cmd.Flags().StringVar(&rulesPath, "league-rules", "", "league alias rules (default clustering.entity_catalog.league.rules_path)")
	cmd.Flags().Float64Var(&frequencyPct, "frequency-pct", 0.5, "list league entities matching in more than this percent of articles")
	return cmd
}
