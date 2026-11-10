// Package evalwiring holds the offline evaluation logic (clustering
// evaluate, dedup evaluate, league backtest) so the CLI and the admin panel
// share one implementation. It must not import the cli tree: the admin
// binary consumes it without cobra globals.
//
// Errors carry no command prefix; callers add their own.
package evalwiring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dedupeval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguebacktest"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguecatalog"
)

// DefaultClusteringDataset is the labeled clustering dataset the evaluate
// command and the admin panel score by default.
const DefaultClusteringDataset = "testdata/clustering_groups.yml"

// DefaultDedupDataset is the labeled dedup dataset the evaluate command and
// the admin panel score by default.
const DefaultDedupDataset = "testdata/dedup_pairs.yml"

// maxLeagueBacktestArticles bounds the league replay (the same order of
// magnitude as the candidate backtest).
const maxLeagueBacktestArticles = 200_000

// defaultLeagueRulesPath is used when neither the options nor the config
// name league alias rules.
const defaultLeagueRulesPath = "configs/league_alias_rules.yml"

// Clustering scores the labeled dataset at datasetPath against the configured
// clustering rules. An empty generatedRulesPath uses
// cfg.Clustering.GeneratedRulesPath. The returned warnings come from loading
// the generated rules; callers decide how to surface them.
func Clustering(cfg *config.Config, datasetPath, generatedRulesPath string) (clustereval.Report, []string, error) {
	rulesFile, err := clusteringrules.LoadWithEntityCatalog(
		cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath,
	)
	if err != nil {
		return clustereval.Report{}, nil, err
	}

	generatedPath := cfg.Clustering.GeneratedRulesPath
	if generatedRulesPath != "" {
		generatedPath = generatedRulesPath
	}
	rulesFile, warnings, err := clusteringrules.LoadWithGenerated(rulesFile, generatedPath)
	if err != nil {
		return clustereval.Report{}, nil, err
	}

	dataset, err := clustereval.LoadDataset(datasetPath)
	if err != nil {
		return clustereval.Report{}, warnings, err
	}

	// now is derived from the dataset itself (its latest published_at)
	// rather than the wall clock, so a given dataset always scores
	// identically regardless of when this runs — the "freshness"/"original
	// content" representative-ranking signals
	// (clustering.RepresentativeConfig) both read now, and this evaluator's
	// whole point is reproducible, deterministic offline scoring (see
	// clustereval.Evaluate's doc comment on the pure-function/no-wall-clock
	// design it mirrors from clustering.DecideJoin/RankRepresentative
	// themselves).
	now := clustereval.LatestPublishedAt(dataset)

	report, err := clustereval.Evaluate(
		dataset,
		rulesFile.ToEventSignalsRules(),
		rulesFile.ToClusteringConfig(),
		rulesFile.ToRepresentativeConfig(),
		now,
	)
	if err != nil {
		return clustereval.Report{}, warnings, err
	}
	return report, warnings, nil
}

// Dedup scores the labeled dataset at datasetPath against the configured
// dedup/clustering thresholds. evaluated is the number of pairs scored, that
// is the dataset size minus the pairs excluded as ambiguous.
func Dedup(cfg *config.Config, datasetPath string) (dedupeval.Report, int, error) {
	dataset, err := dedupeval.LoadDataset(datasetPath)
	if err != nil {
		return dedupeval.Report{}, 0, err
	}

	nd := cfg.Deduplication.NearDuplicate
	thresholdCfg := deduplication.ThresholdConfig{
		DefaultThreshold:      nd.DefaultThreshold,
		ThresholdBySourceType: nd.ThresholdBySourceType,
		ShortTextMaxTokens:    nd.ShortTextMaxTokens,
		ShortTextThreshold:    nd.ShortTextThreshold,
		EventThreshold:        cfg.Clustering.EventThreshold,
		RulesVersion:          nd.RulesVersion,
	}
	report := dedupeval.Evaluate(dataset, cfg.Normalization.Text.Policy(), thresholdCfg, nd.Method)
	return report, len(dataset.Pairs) - report.Excluded, nil
}

// LeagueOptions are the league backtest inputs. Empty RosterPath and
// RulesPath fall back to the configuration.
type LeagueOptions struct {
	// Days is how many days of stored articles to replay; must be positive.
	Days int
	// RosterPath is the nflverse roster CSV.
	RosterPath string
	// RulesPath is the league alias rules file.
	RulesPath string
	// FrequencyPct lists league entities matching in more than this percent
	// of articles.
	FrequencyPct float64
}

// League replays the last opts.Days days of stored articles from db twice,
// without and with the league-wide player rules, and returns the comparison.
// db is only read. The caller decides what a violated invariant means (see
// leaguebacktest.Report.OK).
func League(ctx context.Context, db *sql.DB, cfg *config.Config, opts LeagueOptions, now time.Time) (leaguebacktest.Report, error) {
	if opts.Days <= 0 {
		return leaguebacktest.Report{}, errors.New("--days must be positive")
	}
	lc := cfg.Clustering.EntityCatalog.League
	rosterPath := opts.RosterPath
	if rosterPath == "" {
		rosterPath = lc.RosterPath
	}
	if rosterPath == "" {
		rosterPath = cfg.EntityDiscovery.Roster.Path
	}
	rulesPath := opts.RulesPath
	if rulesPath == "" {
		rulesPath = lc.RulesPath
	}
	if rulesPath == "" {
		rulesPath = defaultLeagueRulesPath
	}
	if rosterPath == "" {
		return leaguebacktest.Report{}, errors.New("no roster (pass --roster or set clustering.entity_catalog.league.roster_path)")
	}

	rulesFile, err := clusteringrules.LoadWithEntityCatalog(
		cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
	if err != nil {
		return leaguebacktest.Report{}, fmt.Errorf("load clustering rules: %w", err)
	}
	// Merged like engine/run.go (empty path: none).
	rulesFile, _, err = clusteringrules.LoadWithGenerated(rulesFile, cfg.Clustering.GeneratedRulesPath)
	if err != nil {
		return leaguebacktest.Report{}, fmt.Errorf("load generated clustering rules: %w", err)
	}
	base := rulesFile.ToEventSignalsRules()

	provider := leaguecatalog.New(leaguecatalog.Config{RosterPath: rosterPath, RulesPath: rulesPath}, base.Entities, discardLogger{})
	provider.Refresh(now)
	if st := provider.Status(); !st.Loaded {
		return leaguebacktest.Report{}, fmt.Errorf("league rules unavailable: %s", st.Reason)
	}

	since := now.AddDate(0, 0, -opts.Days)
	articles, err := sqliteadapter.NewArticleRepository(db).ListSince(ctx, since, maxLeagueBacktestArticles)
	if err != nil {
		return leaguebacktest.Report{}, fmt.Errorf("load articles: %w", err)
	}

	return leaguebacktest.Run(leaguebacktest.Config{
		Articles: articles, Base: base, League: provider.Entities(), Now: now, FrequencyPct: opts.FrequencyPct,
	}), nil
}

// discardLogger drops every log line; the league provider logs only
// diagnostics the report does not need.
type discardLogger struct{}

func (discardLogger) Debug(string, ...any)        {}
func (discardLogger) Info(string, ...any)         {}
func (discardLogger) Warn(string, ...any)         {}
func (discardLogger) Error(string, error, ...any) {}
