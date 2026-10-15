package backtest

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguebacktest"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguecatalog"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

// maxLeagueBacktestArticles bounds the replay (the same order of magnitude as
// the candidate backtest).
const maxLeagueBacktestArticles = 200_000

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)        {}
func (nopLogger) Info(string, ...any)         {}
func (nopLogger) Warn(string, ...any)         {}
func (nopLogger) Error(string, error, ...any) {}

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
			if days <= 0 {
				return fmt.Errorf("backtest league: --days must be positive")
			}
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return fmt.Errorf("backtest league: %w", err)
			}
			lc := cfg.Clustering.EntityCatalog.League
			if rosterPath == "" {
				rosterPath = lc.RosterPath
			}
			if rosterPath == "" {
				rosterPath = cfg.EntityDiscovery.Roster.Path
			}
			if rulesPath == "" {
				rulesPath = lc.RulesPath
			}
			if rulesPath == "" {
				rulesPath = "configs/league_alias_rules.yml"
			}
			if rosterPath == "" {
				return fmt.Errorf("backtest league: no roster (pass --roster or set clustering.entity_catalog.league.roster_path)")
			}

			rulesFile, err := clusteringrules.LoadWithEntityCatalog(
				cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
			if err != nil {
				return fmt.Errorf("backtest league: load clustering rules: %w", err)
			}
			// Merged like engine/run.go (empty path: none).
			rulesFile, _, err = clusteringrules.LoadWithGenerated(rulesFile, cfg.Clustering.GeneratedRulesPath)
			if err != nil {
				return fmt.Errorf("backtest league: load generated clustering rules: %w", err)
			}
			base := rulesFile.ToEventSignalsRules()

			provider := leaguecatalog.New(leaguecatalog.Config{RosterPath: rosterPath, RulesPath: rulesPath}, base.Entities, nopLogger{})
			provider.Refresh(time.Now())
			if st := provider.Status(); !st.Loaded {
				return fmt.Errorf("backtest league: league rules unavailable: %s", st.Reason)
			}

			db, err := infrasqlite.OpenReadOnly(dbPath, 5*time.Second)
			if err != nil {
				return fmt.Errorf("backtest league: open %s read-only: %w", dbPath, err)
			}
			defer db.Close()
			since := time.Now().AddDate(0, 0, -days)
			articles, err := sqliteadapter.NewArticleRepository(db).ListSince(cmd.Context(), since, maxLeagueBacktestArticles)
			if err != nil {
				return fmt.Errorf("backtest league: load articles: %w", err)
			}

			report := leaguebacktest.Run(leaguebacktest.Config{
				Articles: articles, Base: base, League: provider.Entities(), Now: time.Now(), FrequencyPct: frequencyPct,
			})
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
