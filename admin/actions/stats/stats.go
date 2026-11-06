// Package stats holds the admin action over the pipeline health report. It
// mirrors the `stats` CLI command.
package stats

import (
	"context"
	"errors"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/pipelinestats"
)

const group = "stats"

var errBadParams = errors.New("stats: unexpected params type")

type reportParams struct {
	Since      string `json:"since" help:"how far back to report, as a Go duration (for example 12h, 24h)" format:"duration" default:"24h"`
	LogPath    string `json:"log_path" help:"process log to scan for cycle activity; empty uses the host's configured log"`
	TopSources int    `json:"top_sources" help:"how many sources to include in the top-sources breakdown" default:"10"`
}

// Actions returns the stats actions: stats.report.
func Actions() []action.Action {
	return []action.Action{{
		Name: "stats.report", Group: group, Title: "Pipeline stats",
		Description: "Reports pipeline health over a recent window: sources, articles, rejections, Telegram delivery and process-log activity.",
		Kind:        action.KindRead, Modes: []action.Mode{action.ModeLocal, action.ModeProduction},
		Params: reportParams{}, Run: runReport,
	}}
}

func runReport(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reportParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	d, err := parseDuration("since", p.Since)
	if err != nil {
		return action.Result{}, err
	}
	if env.Mode == action.ModeProduction && p.LogPath != "" && p.LogPath != env.StatsLogPath {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: "log_path cannot be changed in production"}
	}
	logPath := p.LogPath
	if logPath == "" {
		logPath = env.StatsLogPath
	}
	now := env.Now()
	report, err := pipelinestats.Build(ctx, sqliteadapter.NewArticleRepository(env.DB), sqliteadapter.NewOutboxRepository(env.DB),
		now.Add(-d), now, logPath, p.TopSources)
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render: "object",
		Object: report,
		Summary: fmt.Sprintf("analyzed=%d accepted=%d rejected=%d reject_rate=%.3f",
			report.ArticlesAnalyzed, report.ArticlesAccepted, report.ArticlesRejected, report.RejectRate),
	}, nil
}

// parseDuration parses a duration param, naming it in the error.
func parseDuration(name, v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf("%s: %v", name, err), Err: err}
	}
	return d, nil
}
