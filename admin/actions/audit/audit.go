// Package audit holds the admin actions over the pipeline decision audit
// trail. They mirror the `audit duplicates` and `audit quality` CLI commands.
package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/decisionaudit"
)

const group = "audit"

var errBadParams = errors.New("audit: unexpected params type")

var bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}

type windowParams struct {
	Days  int `json:"days" help:"how many days back to report" default:"7"`
	Limit int `json:"limit" help:"maximum number of decisions to report" default:"100"`
}

// Actions returns the audit actions: audit.duplicates and audit.quality.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "audit.duplicates", Group: group, Title: "Audit duplicate rejections",
			Description: "Reports recent duplicate-rejection decisions with the reason the pipeline recorded for each.",
			Kind:        action.KindRead, Modes: bothModes, Params: windowParams{}, Run: runDuplicates,
		},
		{
			Name: "audit.quality", Group: group, Title: "Audit editorial quality",
			Description: "Reports editorial decisions with their score and gating threshold, flagging those scored below it.",
			Kind:        action.KindRead, Modes: bothModes, Params: windowParams{}, Run: runQuality,
		},
	}
}

func (p windowParams) validate() error {
	if p.Days <= 0 {
		return &action.Error{Code: action.CodeInvalidParams, Message: "days must be greater than 0"}
	}
	if p.Limit <= 0 {
		return &action.Error{Code: action.CodeInvalidParams, Message: "limit must be greater than 0"}
	}
	return nil
}

func since(env action.Env, days int) time.Time { return env.Now().UTC().AddDate(0, 0, -days) }

func runDuplicates(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*windowParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if err := p.validate(); err != nil {
		return action.Result{}, err
	}
	rows, err := decisionaudit.Duplicates(ctx, sqliteadapter.NewDecisionRepository(env.DB), sqliteadapter.NewArticleRepository(env.DB), since(env, p.Days), p.Limit)
	if err != nil {
		return action.Result{}, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"occurred_at": r.OccurredAt.UTC().Format(time.RFC3339), "article_id": r.ArticleID,
			"title": r.Title, "detail": r.Detail,
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "occurred_at", Label: "When"}, {Key: "article_id", Label: "Article"},
			{Key: "title", Label: "Title"}, {Key: "detail", Label: "Reason"},
		},
		Rows:    out,
		Summary: fmt.Sprintf("duplicate rejections in the last %d days (limit %d): %d total", p.Days, p.Limit, len(rows)),
	}, nil
}

func runQuality(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*windowParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if err := p.validate(); err != nil {
		return action.Result{}, err
	}
	rows, below, err := decisionaudit.Quality(ctx, sqliteadapter.NewDecisionRepository(env.DB), sqliteadapter.NewArticleRepository(env.DB), since(env, p.Days), p.Limit)
	if err != nil {
		return action.Result{}, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"occurred_at": r.OccurredAt.UTC().Format(time.RFC3339), "article_id": r.ArticleID,
			"title": r.Title, "status": r.Status,
			"score": r.Score, "threshold": r.Threshold, "below_threshold": r.BelowThreshold,
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "occurred_at", Label: "When"}, {Key: "article_id", Label: "Article"},
			{Key: "title", Label: "Title"}, {Key: "status", Label: "Status"},
			{Key: "score", Label: "Score"}, {Key: "threshold", Label: "Threshold"},
			{Key: "below_threshold", Label: "Below threshold"},
		},
		Rows:    out,
		Summary: fmt.Sprintf("%d of %d decisions were below their gating threshold", below, len(rows)),
	}, nil
}
