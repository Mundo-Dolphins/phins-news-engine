package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/auth"
)

func (s *server) catalog(w http.ResponseWriter, _ *http.Request) {
	items := []catalogItem{}
	for _, a := range s.d.Registry.For(s.d.Mode) {
		items = append(items, catalogItem{
			Name: a.Name, Group: a.Group, Title: a.Title, Description: a.Description,
			Kind: a.Kind, Modes: a.Modes, LongRunning: a.LongRunning, Exclusive: a.Exclusive,
			Schema: action.Schema(a.Params),
		})
	}
	writeJSON(w, http.StatusOK, items)
}

type runRequest struct {
	Params  json.RawMessage `json:"params"`
	Confirm bool            `json:"confirm"`
}

// availableIn reports whether a is offered in mode.
func availableIn(a action.Action, mode action.Mode) bool {
	for _, m := range a.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

func (s *server) runAction(w http.ResponseWriter, r *http.Request) {
	a, ok := s.d.Registry.Get(r.PathValue("name"))
	if !ok || !availableIn(a, s.d.Mode) {
		writeErr(w, http.StatusNotFound, action.CodeNotFound, "unknown action")
		return
	}
	var req runRequest
	if !readJSON(w, r, &req) {
		return
	}
	if a.Kind == action.KindDestructive && !req.Confirm {
		writeErr(w, http.StatusBadRequest, action.CodeConfirmRequired, "this action is destructive: set confirm to true")
		return
	}
	if s.d.WritesBlocked != nil && a.Kind != action.KindRead {
		writeError(w, s.d.WritesBlocked)
		return
	}
	params, err := action.Decode(a, req.Params)
	if err != nil {
		writeError(w, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	env := s.d.EnvFor("user:" + user)
	if a.LongRunning {
		job, err := s.d.Runner.Start(a, env, params, req.Params)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job": toJob(job)})
		return
	}
	res, err := s.d.Runner.RunSync(r.Context(), a, env, params, req.Params)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}
