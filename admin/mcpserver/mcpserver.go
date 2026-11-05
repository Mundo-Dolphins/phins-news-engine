// Package mcpserver exposes the admin action registry as MCP tools so an AI
// agent can administer the bot. Authentication is the caller's concern.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

// actor is the identity recorded for every MCP-initiated action.
const actor = "mcp"

const (
	defaultJobLimit = 20
	maxJobLimit     = 500
)

// New returns the MCP streamable-HTTP handler. Every action available in
// mode becomes a tool named after the action with "." replaced by "_", plus
// the jobs_get, jobs_list and jobs_cancel tools. Destructive tools require a
// boolean "confirm" argument set to true. When writesBlocked is non-nil all
// non-read actions fail with schema_mismatch.
func New(reg *action.Registry, mode action.Mode, runner *jobs.Runner, st *store.Store, envFor func(actor string) action.Env, writesBlocked error) http.Handler {
	srv := newServer(reg, mode, runner, st, envFor, writesBlocked)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
}

type deps struct {
	runner        *jobs.Runner
	st            *store.Store
	envFor        func(string) action.Env
	writesBlocked error
}

func newServer(reg *action.Registry, mode action.Mode, runner *jobs.Runner, st *store.Store, envFor func(string) action.Env, writesBlocked error) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "phins-news-admin", Version: "0.1.0"}, nil)
	d := &deps{runner: runner, st: st, envFor: envFor, writesBlocked: writesBlocked}
	for _, a := range reg.For(mode) {
		srv.AddTool(actionTool(a), d.actionHandler(a))
	}
	d.addJobTools(srv)
	return srv
}

func toolName(actionName string) string {
	b := []byte(actionName)
	for i, c := range b {
		if c == '.' {
			b[i] = '_'
		}
	}
	return string(b)
}

func actionTool(a action.Action) *mcp.Tool {
	schema := action.Schema(a.Params)
	destructive := a.Kind == action.KindDestructive
	if destructive {
		props, _ := schema["properties"].(map[string]any)
		props["confirm"] = map[string]any{"type": "boolean", "description": "Must be true to perform this destructive action"}
		required, _ := schema["required"].([]string)
		schema["required"] = append(required, "confirm")
	}
	desc := a.Title + "\n\n" + a.Description + "\n\nKind: " + string(a.Kind) + "."
	if a.LongRunning {
		desc += " Long-running: returns a job; poll jobs_get."
	}
	return &mcp.Tool{
		Name:        toolName(a.Name),
		Description: desc,
		InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{
			Title:           a.Title,
			ReadOnlyHint:    a.Kind == action.KindRead,
			DestructiveHint: &destructive,
		},
	}
}

func (d *deps) actionHandler(a action.Action) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := req.Params.Arguments
		if a.Kind == action.KindDestructive {
			var ok bool
			var err error
			if raw, ok, err = takeConfirm(raw); err != nil {
				return toolError(err), nil
			} else if !ok {
				return toolError(&action.Error{Code: action.CodeConfirmRequired, Message: "this action is destructive: set confirm to true"}), nil
			}
		}
		if d.writesBlocked != nil && a.Kind != action.KindRead {
			var ae *action.Error
			if !errors.As(d.writesBlocked, &ae) {
				ae = &action.Error{Code: action.CodeSchemaMismatch, Message: d.writesBlocked.Error()}
			}
			return toolError(ae), nil
		}
		params, err := action.Decode(a, raw)
		if err != nil {
			return toolError(err), nil
		}
		env := d.envFor(actor)
		if a.LongRunning {
			job, err := d.runner.Start(a, env, params, raw)
			if err != nil {
				return toolError(err), nil
			}
			return jsonResult(toJob(job)), nil
		}
		res, err := d.runner.RunSync(ctx, a, env, params, raw)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(res), nil
	}
}

// takeConfirm removes the "confirm" argument from raw and reports whether it
// was true. Anything but a JSON true counts as not confirmed.
func takeConfirm(raw json.RawMessage) (json.RawMessage, bool, error) {
	if len(raw) == 0 {
		return raw, false, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, &action.Error{Code: action.CodeInvalidParams, Message: "arguments must be a JSON object"}
	}
	var confirmed bool
	if c, ok := obj["confirm"]; ok {
		_ = json.Unmarshal(c, &confirmed)
		delete(obj, "confirm")
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false, err
	}
	return out, confirmed, nil
}

func (d *deps) addJobTools(srv *mcp.Server) {
	idSchema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"id": map[string]any{"type": "string", "description": "Job id"}},
		"required":             []string{"id"},
		"additionalProperties": false,
	}
	f := false
	srv.AddTool(&mcp.Tool{
		Name:        "jobs_get",
		Description: "Get a job\n\nReturns the status, result and error of a job started by a long-running action.\n\nKind: read.",
		InputSchema: idSchema,
		Annotations: &mcp.ToolAnnotations{Title: "Get a job", ReadOnlyHint: true, DestructiveHint: &f},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := idArg(req.Params.Arguments)
		if err != nil {
			return toolError(err), nil
		}
		j, ok, err := d.st.GetJob(ctx, id)
		if err != nil {
			return toolError(err), nil
		}
		if !ok {
			return toolError(&action.Error{Code: action.CodeNotFound, Message: "job not found"}), nil
		}
		return jsonResult(toJob(j)), nil
	})
	srv.AddTool(&mcp.Tool{
		Name:        "jobs_list",
		Description: "List jobs\n\nLists the most recent jobs, newest first.\n\nKind: read.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{"limit": map[string]any{
				"type": "integer", "default": defaultJobLimit, "description": "Maximum number of jobs to return",
			}},
			"required":             []string{},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{Title: "List jobs", ReadOnlyHint: true, DestructiveHint: &f},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Limit *int `json:"limit"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return toolError(&action.Error{Code: action.CodeInvalidParams, Message: "limit must be an integer"}), nil
			}
		}
		limit := defaultJobLimit
		if args.Limit != nil {
			limit = min(max(*args.Limit, 1), maxJobLimit)
		}
		js, err := d.st.ListJobs(ctx, limit)
		if err != nil {
			return toolError(err), nil
		}
		out := make([]jobDTO, 0, len(js))
		for _, j := range js {
			out = append(out, toJob(j))
		}
		return jsonResult(out), nil
	})
	t := true
	srv.AddTool(&mcp.Tool{
		Name:        "jobs_cancel",
		Description: "Cancel a job\n\nRequests cancellation of a running job.\n\nKind: write.",
		InputSchema: idSchema,
		Annotations: &mcp.ToolAnnotations{Title: "Cancel a job", DestructiveHint: &t},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := idArg(req.Params.Arguments)
		if err != nil {
			return toolError(err), nil
		}
		if err := d.runner.Cancel(id); err != nil {
			return toolError(err), nil
		}
		return jsonResult(map[string]string{"status": "cancel_requested"}), nil
	})
}

func idArg(raw json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", &action.Error{Code: action.CodeInvalidParams, Message: "id must be a string"}
		}
	}
	if args.ID == "" {
		return "", &action.Error{Code: action.CodeInvalidParams, Message: `field "id" is required`}
	}
	return args.ID, nil
}

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return toolError(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// toolError renders err as a tool error "<code>: <message>". Errors that are
// not action errors are logged and replaced by a generic internal error.
func toolError(err error) *mcp.CallToolResult {
	msg := "internal: internal error"
	var ae *action.Error
	if errors.As(err, &ae) {
		msg = ae.Code + ": " + ae.Error()
	} else {
		slog.Error("admin mcp: internal error", "err", err)
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// jobDTO is the snake_case JSON form of a [store.Job], matching the REST API.
type jobDTO struct {
	ID         string          `json:"id"`
	Action     string          `json:"action"`
	Actor      string          `json:"actor"`
	Status     string          `json:"status"`
	Params     json.RawMessage `json:"params,omitempty"`
	Result     *action.Result  `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

func toJob(j store.Job) jobDTO {
	return jobDTO{ID: j.ID, Action: j.Action, Actor: j.Actor, Status: j.Status, Params: j.Params,
		Result: j.Result, Error: j.Error, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt}
}
