package jobs

import (
	"context"
	"errors"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

// applyLLM applies the admin's LLM session selection to env.Config when an
// action starts, so the run keeps that server even if the selection changes
// meanwhile. It returns the selection in effect, or nil when none applies:
// no session or selection, or params that set their own base URL (the caller
// then owns the whole LLM target and env.Config is left untouched).
//
// The selection is applied for every action, as its config feeds other
// consumers too; only an action with UsesLLM has the server checked, against
// the params' model override when set, else the selected model. A failed check
// or an unknown server is an invalid-params error.
func applyLLM(ctx context.Context, a action.Action, env *action.Env, params any) (*llmservers.Selection, error) {
	if env.LLM == nil {
		return nil, nil
	}
	sel, ok := env.LLM.Current()
	if !ok {
		return nil, nil
	}
	var override string
	if o, ok := params.(action.LLMOverrider); ok {
		var baseURL string
		baseURL, override = o.LLMOverride()
		if baseURL != "" {
			return nil, nil
		}
	}
	if env.Config == nil {
		return nil, errors.New("admin jobs: llm selection set but env.Config is nil")
	}
	eff := sel
	if override != "" {
		eff.Model = override
	}
	cfg, err := env.LLM.Apply(env.Config, eff)
	if err != nil {
		return nil, &action.Error{Code: action.CodeInvalidParams, Message: err.Error()}
	}
	env.Config = cfg
	if a.UsesLLM {
		if err := env.LLM.Check(ctx, eff); err != nil {
			return nil, &action.Error{Code: action.CodeInvalidParams, Message: err.Error()}
		}
	}
	return &eff, nil
}
