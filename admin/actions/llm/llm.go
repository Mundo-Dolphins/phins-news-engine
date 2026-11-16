// Package llm holds the admin actions that choose which configured LLM server
// and model the engine uses. The choice lives in memory in the admin session.
package llm

import (
	"context"
	"errors"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/llmsession"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

const group = "llm"

var errBadParams = errors.New("llm: unexpected params type")

var localOnly = []action.Mode{action.ModeLocal}

type modelsParams struct {
	Server string `json:"server" help:"name of a configured LLM server" required:"true"`
}

type selectParams struct {
	Server string `json:"server" help:"name of a configured LLM server" required:"true"`
	Model  string `json:"model" help:"model id the server lists" required:"true"`
}

// Actions returns llm.status, llm.models, llm.select and llm.clear.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "llm.status", Group: group, Title: "LLM server status",
			Description: "Lists the configured LLM servers and the server and model currently selected, if any.",
			Kind:        action.KindRead, Modes: localOnly, NoOperationalDB: true, Run: runStatus,
		},
		{
			Name: "llm.models", Group: group, Title: "List LLM server models",
			Description: "Lists the models a configured LLM server offers right now.",
			Kind:        action.KindRead, Modes: localOnly, NoOperationalDB: true, Params: modelsParams{}, Run: runModels,
		},
		{
			Name: "llm.select", Group: group, Title: "Select LLM server and model",
			Description: "Makes every later action use the given server and model instead of the configured ones. The choice is kept in memory only.",
			Kind:        action.KindWrite, Modes: localOnly, NoOperationalDB: true, Params: selectParams{}, Run: runSelect,
		},
		{
			Name: "llm.clear", Group: group, Title: "Clear LLM selection",
			Description: "Drops the selected server and model so actions use the configured LLM again.",
			Kind:        action.KindWrite, Modes: localOnly, NoOperationalDB: true, Run: runClear,
		},
	}
}

func invalid(msg string) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: msg}
}

func session(env action.Env) (*llmsession.Session, error) {
	if env.LLM == nil {
		return nil, invalid("no llm_servers configured")
	}
	return env.LLM, nil
}

func statusResult(s *llmsession.Session) action.Result {
	servers := make([]map[string]string, 0, len(s.Servers()))
	for _, srv := range s.Servers() {
		servers = append(servers, map[string]string{"name": srv.Name, "base_url": srv.BaseURL})
	}
	obj := map[string]any{"servers": servers, "selection": nil}
	if sel, ok := s.Current(); ok {
		obj["selection"] = map[string]string{"server": sel.Server, "model": sel.Model}
	}
	return action.Result{Render: "object", Object: obj}
}

func runStatus(_ context.Context, env action.Env, _ any) (action.Result, error) {
	if env.LLM == nil {
		return action.Result{Render: "object", Object: map[string]any{"servers": []map[string]string{}, "selection": nil}}, nil
	}
	return statusResult(env.LLM), nil
}

func runModels(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*modelsParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	s, err := session(env)
	if err != nil {
		return action.Result{}, err
	}
	models, err := s.Models(ctx, p.Server)
	if err != nil {
		return action.Result{}, invalid(err.Error())
	}
	return action.Result{Render: "object", Object: map[string]any{"server": p.Server, "models": models}}, nil
}

func runSelect(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*selectParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	s, err := session(env)
	if err != nil {
		return action.Result{}, err
	}
	if err := s.Select(ctx, llmservers.Selection{Server: p.Server, Model: p.Model}); err != nil {
		return action.Result{}, invalid(err.Error())
	}
	return statusResult(s), nil
}

func runClear(_ context.Context, env action.Env, _ any) (action.Result, error) {
	s, err := session(env)
	if err != nil {
		return action.Result{}, err
	}
	s.Clear()
	return statusResult(s), nil
}
