// Package label holds the admin actions over human labels: setting a label,
// resolving a legacy label conflict, and reading history and conflicts. They
// mirror the `label set`, `label resolve-conflict`, `label history` and
// `label conflicts` CLI commands.
package label

import (
	"context"
	"errors"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/humanlabels"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

const group = "label"

var errBadParams = errors.New("label: unexpected params type")

var bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}

type setParams struct {
	ArticleID string `json:"article_id" help:"article to label" required:"true"`
	Label     string `json:"label" help:"the label to apply" enum:"publish,reject" required:"true"`
	By        string `json:"by" help:"who applied this label (defaults to the signed-in user)"`
	Notes     string `json:"notes" help:"free-text notes for this label"`
}

type historyParams struct {
	ArticleID string `json:"article_id" help:"article whose label history to show" required:"true"`
}

type noParams struct{}

// Actions returns the label actions: label.set, label.resolve_conflict,
// label.history and label.conflicts.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "label.set", Group: group, Title: "Label article",
			Description: "Human-labels an article publish or reject, relabeling (new revision) when it already has an active label; refuses articles with an unresolved legacy conflict.",
			Kind:        action.KindWrite, Modes: bothModes, Params: setParams{}, Run: runSet,
		},
		{
			Name: "label.resolve_conflict", Group: group, Title: "Resolve label conflict",
			Description: "Resolves an unresolved legacy label conflict by choosing the winning label.",
			Kind:        action.KindWrite, Modes: bothModes, Params: setParams{}, Run: runResolve,
		},
		{
			Name: "label.history", Group: group, Title: "Label history",
			Description: "Shows every label revision of an article, active and superseded.",
			Kind:        action.KindRead, Modes: bothModes, Params: historyParams{}, Run: runHistory,
		},
		{
			Name: "label.conflicts", Group: group, Title: "Label conflicts",
			Description: "Lists articles with an unresolved legacy label conflict.",
			Kind:        action.KindRead, Modes: bothModes, Params: noParams{}, Run: runConflicts,
		},
	}
}

func example(env action.Env, p *setParams, source string) training.Example {
	by := p.By
	if by == "" {
		by = env.Actor
	}
	return training.Example{
		ArticleID: p.ArticleID, Label: p.Label, LabeledBy: by, LabeledAt: env.Now().UTC(), Notes: p.Notes, Source: source,
	}
}

func checkLabel(l string) error {
	if l != "publish" && l != "reject" {
		err := fmt.Errorf("label: invalid label %q (want \"publish\" or \"reject\")", l)
		return &action.Error{Code: action.CodeInvalidParams, Message: err.Error(), Err: err}
	}
	return nil
}

func runSet(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*setParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if err := checkLabel(p.Label); err != nil {
		return action.Result{}, err
	}
	out, warning, err := humanlabels.Set(ctx, sqliteadapter.NewHumanLabelRepository(env.DB), sqliteadapter.NewHardExampleRepository(env.DB),
		example(env, p, "admin:label"))
	if errors.Is(err, humanlabels.ErrUnresolvedConflict) {
		return action.Result{}, &action.Error{Code: action.CodeConflict, Err: err,
			Message: p.ArticleID + " has an unresolved legacy label conflict; use label.resolve_conflict"}
	}
	if errors.Is(err, training.ErrNoSuchArticle) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	if warning != nil {
		env.Log.Warn("label set", "warning", warning.Error())
	}
	msg := fmt.Sprintf("labeled %s as %s", p.ArticleID, p.Label)
	if out.Relabeled {
		msg = fmt.Sprintf("relabeled %s as %s (was %s, now revision %d)", p.ArticleID, p.Label, out.Previous, out.Revision)
	}
	env.Log.Info("label set", "article", p.ArticleID, "label", p.Label, "by", env.Actor)
	return action.Result{Render: "text", Text: msg}, nil
}

func runResolve(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*setParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if err := checkLabel(p.Label); err != nil {
		return action.Result{}, err
	}
	warning, err := humanlabels.ResolveConflict(ctx, sqliteadapter.NewHumanLabelRepository(env.DB), sqliteadapter.NewHardExampleRepository(env.DB),
		example(env, p, "admin:label-resolve-conflict"))
	if err != nil {
		if errors.Is(err, training.ErrNoUnresolvedConflict) || errors.Is(err, training.ErrNoSuchArticle) {
			return action.Result{}, action.NotFound(err)
		}
		return action.Result{}, err
	}
	if warning != nil {
		env.Log.Warn("label resolve_conflict", "warning", warning.Error())
	}
	env.Log.Info("label conflict resolved", "article", p.ArticleID, "label", p.Label, "by", env.Actor)
	return action.Result{Render: "text", Text: fmt.Sprintf("resolved conflict for %s as %s", p.ArticleID, p.Label)}, nil
}

func runHistory(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*historyParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	history, err := humanlabels.History(ctx, sqliteadapter.NewHumanLabelRepository(env.DB), p.ArticleID)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(history))
	for _, e := range history {
		status := "superseded"
		if e.Active {
			status = "active"
		}
		rows = append(rows, map[string]any{
			"rev": e.Revision, "status": status, "label": e.Label, "by": orDash(e.LabeledBy),
			"at": e.LabeledAt.UTC().Format(time.RFC3339), "source": orDash(e.Source), "reason": orDash(e.Reason),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "rev", Label: "Rev"}, {Key: "status", Label: "Status"}, {Key: "label", Label: "Label"},
			{Key: "by", Label: "By"}, {Key: "at", Label: "At"}, {Key: "source", Label: "Source"}, {Key: "reason", Label: "Reason"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d revision(s) for %s", len(rows), p.ArticleID),
	}, nil
}

func runConflicts(ctx context.Context, env action.Env, params any) (action.Result, error) {
	if _, ok := params.(*noParams); !ok {
		return action.Result{}, errBadParams
	}
	conflicts, err := sqliteadapter.NewHumanLabelRepository(env.DB).ListUnresolvedConflicts(ctx)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(conflicts))
	for _, c := range conflicts {
		rows = append(rows, map[string]any{"article_id": c.ArticleID, "table": c.TableName, "detail": orDash(c.Detail)})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "article_id", Label: "Article"}, {Key: "table", Label: "Table"}, {Key: "detail", Label: "Detail"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d unresolved conflict(s)", len(rows)),
	}, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
