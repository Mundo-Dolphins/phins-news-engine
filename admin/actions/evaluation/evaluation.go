// Package evaluation holds the admin actions over the offline evaluations:
// the clustering and dedup scores against their labeled datasets and the
// candidate and league backtests over the stored history. They mirror the
// `clustering evaluate`, `dedup evaluate`, `backtest candidate` and
// `backtest league` CLI commands and share their logic. The two dataset
// evaluations are available in both modes; the backtests are local-only.
package evaluation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/backtestcandidate"
	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dedupeval"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/evalwiring"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

const backtestGroup = "backtest"

// defaultCandidateOut is the CLI default for the candidate backtest CSV.
const defaultCandidateOut = "data/backtest-candidate-report.csv"

var errBadParams = errors.New("evaluation: unexpected params type")

// leagueRun is the league wiring; a test replaces it to force a report the
// real wiring cannot produce (league rules are always scoped, so the
// invariant cannot be broken through the file-based inputs).
var leagueRun = evalwiring.League

var (
	bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}
	localOnly = []action.Mode{action.ModeLocal}
)

type clusteringParams struct {
	Dataset        string `json:"dataset" help:"labeled clustering dataset to score" default:"testdata/clustering_groups.yml" format:"path"`
	GeneratedRules string `json:"generated_rules" help:"generated event rules file that replaces clustering.generated_rules_path" format:"path"`
}

type dedupParams struct {
	Dataset string `json:"dataset" help:"labeled dedup dataset to score" default:"testdata/dedup_pairs.yml" format:"path"`
}

type candidateParams struct {
	Days                int    `json:"days" help:"days of stored history to replay (1 or more)" default:"30"`
	CandidateDictionary string `json:"candidate_dictionary" help:"candidate dictionary YAML to evaluate in place of the promoted one" format:"path"`
	CandidateModel      string `json:"candidate_model" help:"candidate editorial model directory to evaluate in place of the promoted one" format:"path"`
	Out                 string `json:"out" help:"CSV file the per-article detail is written to" default:"data/backtest-candidate-report.csv" format:"path"`
}

type leagueParams struct {
	Days         int     `json:"days" help:"days of stored articles to replay (1 or more)" default:"14"`
	Roster       string  `json:"roster" help:"nflverse roster CSV (default clustering.entity_catalog.league.roster_path)" format:"path"`
	LeagueRules  string  `json:"league_rules" help:"league alias rules (default clustering.entity_catalog.league.rules_path)" format:"path"`
	FrequencyPct float64 `json:"frequency_pct" help:"list league entities matching in more than this percent of articles" default:"0.5"`
}

// Actions returns clustering.evaluate, dedup.evaluate, backtest.candidate
// and backtest.league.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "clustering.evaluate", Group: "clustering", Title: "Evaluate clustering",
			Description: "Scores the labeled clustering dataset against the configured clustering rules: pairwise precision/recall, purity, over-grouped and fragmented clusters, representative and role accuracy, and every article's predicted placement. Nothing is written.",
			Kind:        action.KindRead, Modes: bothModes, LongRunning: true, Params: clusteringParams{}, Run: runClustering,
		},
		{
			Name: "dedup.evaluate", Group: "dedup", Title: "Evaluate deduplication",
			Description: "Scores the labeled dedup pairs against the configured dedup and clustering thresholds: overall, per source and content type metrics and the confusion matrix. Nothing is written.",
			Kind:        action.KindRead, Modes: bothModes, LongRunning: true, Params: dedupParams{}, Run: runDedup,
		},
		{
			Name: "backtest.candidate", Group: backtestGroup, Title: "Backtest candidate",
			Description: "Replays the stored history through a candidate dictionary and/or editorial model against a scratch database and counts what it would publish or reject differently. Writes the per-article CSV; the operational database is only read.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Params: candidateParams{}, Run: runCandidate,
		},
		{
			Name: "backtest.league", Group: backtestGroup, Title: "Backtest league rules",
			Description: "Replays the stored articles without and with the league-wide player rules and proves that nothing but the entity mentions changes. A violated invariant is reported in the result (FAILED), not as an error. Nothing is written.",
			Kind:        action.KindRead, Modes: localOnly, LongRunning: true, Params: leagueParams{}, Run: runLeague,
		},
	}
}

func invalid(format string, args ...any) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

// datasetError maps a missing dataset file to NotFound.
func datasetError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return action.NotFound(err)
	}
	return err
}

func runClustering(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*clusteringParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("clustering: no configuration loaded")
	}
	if err := action.RefusePathOverrides(env,
		action.PathParam{Name: "dataset", Value: p.Dataset, Configured: evalwiring.DefaultClusteringDataset},
		action.PathParam{Name: "generated_rules", Value: p.GeneratedRules, Configured: env.Config.Clustering.GeneratedRulesPath},
	); err != nil {
		return action.Result{}, err
	}
	report, warnings, err := evalwiring.Clustering(env.Config, p.Dataset, p.GeneratedRules)
	for _, w := range warnings {
		env.Log.Warn(w)
	}
	if err != nil {
		return action.Result{}, datasetError(err)
	}
	return action.Result{
		Render:  "object",
		Object:  clusteringObject(report),
		Summary: fmt.Sprintf("%d articles scored, pairwise f1 %.3f", report.ArticlesScored, report.Pairwise.F1()),
	}, nil
}

func clusteringObject(r clustereval.Report) map[string]any {
	assignments := make([]map[string]any, 0, len(r.Assignments))
	for _, a := range r.Assignments {
		assignments = append(assignments, map[string]any{
			"article_id": a.ArticleID, "gold_group": a.GoldGroupID, "predicted_cluster": a.PredictedClusterID,
			"action": string(a.Action), "role": string(a.MemberRole), "excluded": a.Excluded,
		})
	}
	obj := map[string]any{
		"articles_total": r.ArticlesTotal, "articles_scored": r.ArticlesScored, "ambiguous_excluded": r.AmbiguousExpected,
		"pairwise": map[string]any{
			"precision": r.Pairwise.Precision(), "recall": r.Pairwise.Recall(), "f1": r.Pairwise.F1(),
			"tp": r.Pairwise.TP, "fp": r.Pairwise.FP, "fn": r.Pairwise.FN, "tn": r.Pairwise.TN,
		},
		"purity":                 r.Purity,
		"over_grouped_clusters":  r.OverGroupedClusters,
		"fragmented_clusters":    r.FragmentedClusters,
		"representative_correct": r.RepresentativeCorrect, "representative_total": r.RepresentativeTotal,
		"representative_accuracy": r.RepresentativeAccuracy,
		"ambiguous": map[string]any{
			"expected": r.AmbiguousExpected, "correctly_flagged": r.AmbiguousCorrectlyFlagged, "unexpected": r.UnexpectedAmbiguous,
		},
		"assignments": assignments,
	}
	if r.RoleTotal > 0 {
		obj["role_accuracy"] = float64(r.RoleCorrect) / float64(r.RoleTotal)
		obj["role_correct"] = r.RoleCorrect
		obj["role_total"] = r.RoleTotal
	}
	return obj
}

func runDedup(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*dedupParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("dedup: no configuration loaded")
	}
	if err := action.RefusePathOverrides(env,
		action.PathParam{Name: "dataset", Value: p.Dataset, Configured: evalwiring.DefaultDedupDataset},
	); err != nil {
		return action.Result{}, err
	}
	report, evaluated, err := evalwiring.Dedup(env.Config, p.Dataset)
	if err != nil {
		return action.Result{}, datasetError(err)
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"evaluated": evaluated, "excluded": report.Excluded,
			"overall":         metricsOf(report.Overall),
			"by_source_type":  metricsByKey(report.BySourceType),
			"by_content_type": metricsByKey(report.ByContentType),
			"confusion":       confusionRows(report.Confusion),
		},
		Summary: fmt.Sprintf("%d pairs evaluated, %d excluded, f1 %.3f", evaluated, report.Excluded, report.Overall.F1()),
	}, nil
}

func metricsOf(m dedupeval.BinaryMetrics) map[string]any {
	return map[string]any{
		"precision": m.Precision(), "recall": m.Recall(), "f1": m.F1(),
		"tp": m.TP, "fp": m.FP, "fn": m.FN, "tn": m.TN,
	}
}

// metricsByKey lists the metrics of each key, keys sorted.
func metricsByKey(in map[string]dedupeval.BinaryMetrics) []map[string]any {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		row := metricsOf(in[k])
		row["key"] = k
		out = append(out, row)
	}
	return out
}

// confusionRows flattens the predicted x gold matrix, sorted by predicted
// then gold.
func confusionRows(in map[string]map[string]int) []map[string]any {
	out := []map[string]any{}
	predicted := make([]string, 0, len(in))
	for k := range in {
		predicted = append(predicted, k)
	}
	sort.Strings(predicted)
	for _, pr := range predicted {
		golds := make([]string, 0, len(in[pr]))
		for g := range in[pr] {
			golds = append(golds, g)
		}
		sort.Strings(golds)
		for _, g := range golds {
			out = append(out, map[string]any{"predicted": pr, "gold": g, "count": in[pr][g]})
		}
	}
	return out
}

func runCandidate(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*candidateParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("backtest: no configuration loaded")
	}
	if p.Days <= 0 {
		return action.Result{}, invalid("days must be positive")
	}
	if p.CandidateDictionary == "" && p.CandidateModel == "" {
		return action.Result{}, invalid("at least one of candidate_dictionary or candidate_model is required")
	}
	out := p.Out
	if out == "" {
		out = defaultCandidateOut
	}
	report, err := backtestcandidate.Run(ctx, backtestcandidate.Config{
		Days: p.Days, DBPath: env.Config.SQLite.Path,
		CandidateDictPath: p.CandidateDictionary, CandidateModelPath: p.CandidateModel,
		V2ConfigPath: env.ConfigPath, OutPath: out,
		Progress: func(message string) { env.Log.Info("backtest candidate progress", "message", message) },
	})
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"days": p.Days, "total_articles": report.TotalArticles,
			"regressions": report.Regressions, "new_passes": report.NewPasses,
			"agree_publish": report.AgreePublish, "agree_reject": report.AgreeReject, "out": out,
		},
		Summary: fmt.Sprintf("%d articles analyzed: %d regressions, %d new passes", report.TotalArticles, report.Regressions, report.NewPasses),
	}, nil
}

func runLeague(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*leagueParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("backtest: no configuration loaded")
	}
	if p.Days <= 0 {
		return action.Result{}, invalid("days must be positive")
	}
	// Opened read-only like the CLI, not env.DB: the backtest never shares
	// the panel's write connection.
	db, err := infrasqlite.OpenReadOnly(env.Config.SQLite.Path, 5*time.Second)
	if err != nil {
		return action.Result{}, fmt.Errorf("open %s read-only: %w", env.Config.SQLite.Path, err)
	}
	defer db.Close()

	report, err := leagueRun(ctx, db, env.Config, evalwiring.LeagueOptions{
		Days: p.Days, RosterPath: p.Roster, RulesPath: p.LeagueRules, FrequencyPct: p.FrequencyPct,
	}, env.Now())
	if err != nil {
		return action.Result{}, err
	}
	summary := "OK"
	if !report.OK() {
		summary = fmt.Sprintf("FAILED: invariant violated for %d articles", len(report.InvariantViolations))
	}
	return action.Result{Render: "text", Text: report.Text(), Summary: summary}, nil
}
