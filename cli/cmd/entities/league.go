// cli/cmd/entities/league.go
package entities

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguerules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// loadLeaguePlayers reads the local roster. A disabled block, or a missing, stale
// or corrupt file, yields nil players and a warning: team logic and the noise
// filter still work without a roster.
func loadLeaguePlayers(cfg config.RosterConfig, now time.Time, logger appout.Logger) []leaguegazetteer.Player {
	if !cfg.Enabled {
		return nil
	}
	info, err := os.Stat(cfg.Path)
	if err != nil {
		logger.Warn("nflverse roster unavailable; run `entities roster sync`", "path", cfg.Path, "error", err)
		return nil
	}
	if cfg.MaxAgeDays > 0 && now.Sub(info.ModTime()) > time.Duration(cfg.MaxAgeDays)*24*time.Hour {
		logger.Warn("nflverse roster is stale; player tiers disabled, run `entities roster sync`", "path", cfg.Path, "modified", info.ModTime().Format(time.RFC3339), "max_age_days", cfg.MaxAgeDays)
		return nil
	}
	f, err := os.Open(cfg.Path)
	if err != nil {
		logger.Warn("nflverse roster unreadable", "path", cfg.Path, "error", err)
		return nil
	}
	defer f.Close()
	rows, err := nflverse.ParseRoster(f)
	if err != nil {
		logger.Warn("nflverse roster invalid; player tiers disabled", "path", cfg.Path, "error", err)
		return nil
	}
	out := make([]leaguegazetteer.Player, len(rows))
	for i, p := range rows {
		out[i] = leaguegazetteer.Player{
			FullName: p.FullName, FirstName: p.FirstName, LastName: p.LastName, FootballName: p.FootballName,
			TeamAbbr: p.Team, Position: p.Position, Status: p.Status, GSISID: p.GSISID,
		}
	}
	return out
}

// buildLeagueStage assembles the gazetteer stage. It returns nil when the
// league block is disabled; with it enabled, an invalid rules file or a rules
// team missing from the catalog is a start-up error (never a silent downgrade).
func buildLeagueStage(cfg *config.Config, catalog entitydiscovery.CatalogIndex, now time.Time, logger appout.Logger) (*entitydiscovery.LeagueStage, error) {
	if !cfg.EntityDiscovery.League.Enabled {
		return nil, nil
	}
	rules, err := leaguerules.Load(cfg.EntityDiscovery.League.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("entity_discovery.league: %w", err)
	}
	idx, err := leaguegazetteer.New(loadLeaguePlayers(cfg.EntityDiscovery.Roster, now, logger), rules)
	if err != nil {
		return nil, fmt.Errorf("entity_discovery.league: %w", err)
	}
	stage, err := entitydiscovery.NewLeagueStage(idx, catalog)
	if err != nil {
		return nil, fmt.Errorf("entity_discovery.league: %w", err)
	}
	return stage, nil
}

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
			catalogIdx, err := loadCatalogIndex(manualPath, generatedPath)
			if err != nil {
				return fmt.Errorf("entities league evaluate: %w", err)
			}
			stage, err := buildLeagueStage(cfg, catalogIdx, time.Now(), discoveryLogger{out: cmd.OutOrStderr(), component: "league"})
			if err != nil {
				return fmt.Errorf("entities league evaluate: %w", err)
			}
			if stage == nil {
				return fmt.Errorf("entities league evaluate: entity_discovery.league is disabled")
			}
			stage.Texts = articleTexts{finder: sqliteadapter.NewArticleRepository(db)}
			rep, err := entitydiscovery.EvaluateLeague(cmd.Context(), sqliteentityproposal.NewEntityProposalRepository(db), stage)
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
