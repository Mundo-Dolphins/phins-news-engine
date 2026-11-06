// Package claims holds the admin actions over event claims (ADR 0034):
// listing, showing, retrying and releasing them. They mirror the
// `event claims` CLI commands.
package claims

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventclaims"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const group = "claims"

var errBadParams = errors.New("claims: unexpected params type")

var bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}

type listParams struct {
	Handler string `json:"handler" help:"only claims of this handler"`
	State   string `json:"state" help:"only claims in this state" enum:"claimed,published,failed"`
	Since   string `json:"since" help:"only claims created since a duration ago (24h) or a date (2026-11-01)" format:"duration|date"`
}

type keyParams struct {
	Handler string `json:"handler" help:"handler that owns the claim" required:"true"`
	Key     string `json:"key" help:"claim key" required:"true"`
}

type retryParams struct {
	Handler string `json:"handler" help:"handler that owns the claim" required:"true"`
	Key     string `json:"key" help:"claim key" required:"true"`
	DryRun  bool   `json:"dry_run" help:"check whether the retry would be accepted without changing anything"`
}

// Actions returns the claims actions: claims.list, claims.show,
// claims.retry and claims.release.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "claims.list", Group: group, Title: "List event claims",
			Description: "Lists event claims with their outbox status and last error.",
			Kind:        action.KindRead, Modes: bothModes, Params: listParams{}, Run: runList,
		},
		{
			Name: "claims.show", Group: group, Title: "Show event claim",
			Description: "Shows one event claim with its full JSON data and outbox rows.",
			Kind:        action.KindRead, Modes: bothModes, Params: keyParams{}, Run: runShow,
		},
		{
			Name: "claims.retry", Group: group, Title: "Retry failed claim",
			Description: "Puts the dead outbox rows of a failed claim back to pending and sets the claim to claimed; refuses published claims and claims with an in-flight outbox row.",
			Kind:        action.KindWrite, Modes: bothModes, Params: retryParams{}, Run: runRetry,
		},
		{
			Name: "claims.release", Group: group, Title: "Release claim",
			Description: "Deletes a claimed or failed claim (never a published one) so the handler can claim the key again.",
			Kind:        action.KindDestructive, Modes: bothModes, Params: keyParams{}, Run: runRelease,
		},
	}
}

func runList(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*listParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	filter := appout.EventClaimFilter{Handler: p.Handler, State: appout.EventClaimState(p.State)}
	var since time.Time
	if p.Since != "" {
		t, err := eventclaims.ParseSince(p.Since, env.Now())
		if err != nil {
			return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: err.Error(), Err: err}
		}
		since = t
	}
	views, err := eventclaims.List(ctx, sqliteadapter.NewEventClaimRepository(env.DB), filter, since)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(views))
	for _, v := range views {
		rows = append(rows, map[string]any{
			"handler":    v.Handler,
			"key":        v.Key,
			"state":      string(v.State),
			"article":    orDash(v.ArticleID),
			"outbox":     outboxSummary(v.Outbox),
			"last_error": orDash(v.LastError),
			"created_at": v.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "handler", Label: "Handler"}, {Key: "key", Label: "Key"}, {Key: "state", Label: "State"},
			{Key: "article", Label: "Article"}, {Key: "outbox", Label: "Outbox"},
			{Key: "last_error", Label: "Last error"}, {Key: "created_at", Label: "Created"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d claim(s)", len(rows)),
	}, nil
}

type outboxJSON struct {
	Destination string `json:"destination"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"last_error,omitempty"`
}

type claimJSON struct {
	Handler   string          `json:"handler"`
	Key       string          `json:"key"`
	State     string          `json:"state"`
	ArticleID string          `json:"article_id"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Data      json.RawMessage `json:"data"`
	Outbox    []outboxJSON    `json:"outbox"`
}

func runShow(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*keyParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	v, err := eventclaims.Show(ctx, sqliteadapter.NewEventClaimRepository(env.DB), p.Handler, p.Key)
	if errors.Is(err, appout.ErrEventClaimNotFound) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	obj := claimJSON{v.Handler, v.Key, string(v.State), v.ArticleID, v.CreatedAt, v.UpdatedAt, v.Data, []outboxJSON{}}
	for _, r := range v.Outbox {
		obj.Outbox = append(obj.Outbox, outboxJSON{r.Destination, r.Status, r.Attempts, r.LastError})
	}
	return action.Result{Render: "object", Object: obj}, nil
}

func runRetry(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*retryParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	repo := sqliteadapter.NewEventClaimRepository(env.DB)
	if p.DryRun {
		dead, err := eventclaims.CheckRetry(ctx, repo, p.Handler, p.Key)
		if err != nil {
			return action.Result{}, retryError(p.Handler, p.Key, err)
		}
		return action.Result{Render: "text", Text: fmt.Sprintf(
			"dry run: would re-enqueue %d outbox row(s) of %s/%s and set the claim to claimed", dead, p.Handler, p.Key)}, nil
	}
	n, err := repo.RetryFailed(ctx, p.Handler, p.Key, env.Now())
	if err != nil {
		return action.Result{}, retryError(p.Handler, p.Key, err)
	}
	return action.Result{Render: "text", Text: fmt.Sprintf(
		"retry %s/%s: %d outbox row(s) re-enqueued, claim set to claimed", p.Handler, p.Key, n)}, nil
}

func retryError(handler, key string, err error) error {
	switch {
	case errors.Is(err, appout.ErrEventClaimNotFound):
		return action.NotFound(fmt.Errorf("no claim %s/%s", handler, key))
	case errors.Is(err, appout.ErrEventClaimNotFailed):
		return action.Conflict(fmt.Errorf("refused, %s/%s is not failed (only failed claims can be retried)", handler, key))
	case errors.Is(err, appout.ErrEventClaimInFlight):
		return action.Conflict(fmt.Errorf("refused, %s/%s still has an in-flight outbox row (let it finish or use the outbox repair command)", handler, key))
	case errors.Is(err, appout.ErrEventClaimNothingToRetry):
		return action.Conflict(fmt.Errorf("refused, %s/%s has no dead outbox row to retry (release it instead)", handler, key))
	}
	return err
}

func runRelease(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*keyParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	released, err := sqliteadapter.NewEventClaimRepository(env.DB).Release(ctx, p.Handler, p.Key)
	if errors.Is(err, appout.ErrEventClaimPublished) {
		return action.Result{}, action.Conflict(fmt.Errorf("refused, %s/%s is published and would allow a duplicate post", p.Handler, p.Key))
	}
	if err != nil {
		return action.Result{}, err
	}
	if !released {
		return action.Result{}, action.NotFound(fmt.Errorf("no claim %s/%s", p.Handler, p.Key))
	}
	env.Log.Info("event claim released", "handler", p.Handler, "key", p.Key, "by", env.Actor)
	at := env.Now().UTC().Format(time.RFC3339)
	return action.Result{Render: "text", Text: fmt.Sprintf("released %s/%s (by %s at %s)", p.Handler, p.Key, env.Actor, at)}, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func outboxSummary(rows []appout.EventClaimOutboxRow) string {
	if len(rows) == 0 {
		return "none"
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = r.Destination + ":" + r.Status
	}
	return strings.Join(parts, ",")
}
