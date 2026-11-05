package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

type echoParams struct {
	Text string `json:"text" help:"text to echo" required:"true"`
}

type wipeParams struct {
	Name string `json:"name" required:"true"`
}

type fixture struct {
	cs    *mcp.ClientSession
	wiped atomic.Int32
	st    *store.Store
}

func setup(t *testing.T, blocked error) *fixture {
	t.Helper()
	f := &fixture{}
	reg := &action.Registry{}
	local := []action.Mode{action.ModeLocal}
	reg.Add(
		action.Action{Name: "fake.echo", Group: "fake", Title: "Echo", Description: "Echoes text.", Kind: action.KindRead, Modes: local,
			Params: echoParams{}, Run: func(_ context.Context, env action.Env, p any) (action.Result, error) {
				ep, _ := p.(*echoParams)
				return action.Result{Render: "text", Text: ep.Text + "|" + env.Actor}, nil
			}},
		action.Action{Name: "fake.wipe", Group: "fake", Title: "Wipe", Description: "Wipes.", Kind: action.KindDestructive, Modes: local,
			Params: wipeParams{}, Run: func(context.Context, action.Env, any) (action.Result, error) {
				f.wiped.Add(1)
				return action.Result{Render: "text", Text: "wiped"}, nil
			}},
		action.Action{Name: "fake.slow", Group: "fake", Title: "Slow", Description: "Long.", Kind: action.KindWrite, Modes: local, LongRunning: true,
			Run: func(context.Context, action.Env, any) (action.Result, error) {
				return action.Result{Render: "text", Text: "done"}, nil
			}},
		action.Action{Name: "fake.prod", Group: "fake", Title: "Prod", Kind: action.KindRead, Modes: []action.Mode{action.ModeProduction},
			Run: func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil }},
	)
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.st = st
	runner, err := jobs.New(ctx, st, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); runner.Wait(); _ = st.Close() })
	srv := newServer(reg, action.ModeLocal, runner, st, func(actor string) action.Env { return action.Env{Actor: actor} }, blocked)
	ct, stt := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stt, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	f.cs = cs
	return f
}

func (f *fixture) call(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := f.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content len = %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T", res.Content[0])
	}
	return tc.Text
}

func TestListToolsMatchesModeCatalog(t *testing.T) {
	f := setup(t, nil)
	res, err := f.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		tools[tl.Name] = tl
	}
	for _, n := range []string{"fake_echo", "fake_wipe", "fake_slow", "jobs_get", "jobs_list", "jobs_cancel"} {
		if tools[n] == nil {
			t.Errorf("missing tool %s", n)
		}
	}
	if tools["fake_prod"] != nil || len(tools) != 6 {
		t.Errorf("unexpected tools: %d", len(tools))
	}
	echo := tools["fake_echo"]
	if echo.Description != "Echo\n\nEchoes text.\n\nKind: read." {
		t.Errorf("description = %q", echo.Description)
	}
	if !echo.Annotations.ReadOnlyHint || *echo.Annotations.DestructiveHint || echo.Annotations.Title != "Echo" {
		t.Errorf("echo annotations = %+v", echo.Annotations)
	}
	if d := tools["fake_slow"].Description; !strings.HasSuffix(d, "Kind: write. Long-running: returns a job; poll jobs_get.") {
		t.Errorf("slow description = %q", d)
	}
	wipe := tools["fake_wipe"]
	if !*wipe.Annotations.DestructiveHint || wipe.Annotations.ReadOnlyHint {
		t.Errorf("wipe annotations = %+v", wipe.Annotations)
	}
	b, _ := json.Marshal(wipe.InputSchema)
	var sch struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	_ = json.Unmarshal(b, &sch)
	if sch.Properties["confirm"]["type"] != "boolean" || sch.Properties["name"] == nil {
		t.Errorf("wipe schema = %s", b)
	}
	if len(sch.Required) != 2 {
		t.Errorf("wipe required = %v", sch.Required)
	}
	if !tools["jobs_get"].Annotations.ReadOnlyHint || tools["jobs_cancel"].Annotations.ReadOnlyHint {
		t.Error("jobs annotations wrong")
	}
}

func TestCallReadTool(t *testing.T) {
	f := setup(t, nil)
	res := f.call(t, "fake_echo", map[string]any{"text": "hi"})
	if res.IsError {
		t.Fatalf("error: %s", text(t, res))
	}
	var r action.Result
	if err := json.Unmarshal([]byte(text(t, res)), &r); err != nil || r.Text != "hi|mcp" {
		t.Errorf("result = %+v err=%v", r, err)
	}
}

func TestInvalidParamsIsToolError(t *testing.T) {
	f := setup(t, nil)
	res := f.call(t, "fake_echo", map[string]any{})
	if !res.IsError || !strings.HasPrefix(text(t, res), "invalid_params: ") {
		t.Errorf("got %v %q", res.IsError, text(t, res))
	}
}

func TestDestructiveWithoutConfirmIsToolError(t *testing.T) {
	f := setup(t, nil)
	for _, args := range []map[string]any{{"name": "x"}, {"name": "x", "confirm": false}} {
		res := f.call(t, "fake_wipe", args)
		if !res.IsError || !strings.HasPrefix(text(t, res), "confirmation_required: ") {
			t.Errorf("args %v: %v %q", args, res.IsError, text(t, res))
		}
	}
	if f.wiped.Load() != 0 {
		t.Fatal("destructive action executed without confirm")
	}
	res := f.call(t, "fake_wipe", map[string]any{"name": "x", "confirm": true})
	if res.IsError || f.wiped.Load() != 1 {
		t.Errorf("confirmed call failed: %v %q wiped=%d", res.IsError, text(t, res), f.wiped.Load())
	}
}

func TestWritesBlocked(t *testing.T) {
	f := setup(t, errors.New("schema drift"))
	res := f.call(t, "fake_wipe", map[string]any{"name": "x", "confirm": true})
	if !res.IsError || !strings.HasPrefix(text(t, res), "schema_mismatch: ") || f.wiped.Load() != 0 {
		t.Errorf("got %v %q wiped=%d", res.IsError, text(t, res), f.wiped.Load())
	}
	if res := f.call(t, "fake_echo", map[string]any{"text": "ok"}); res.IsError {
		t.Errorf("read blocked: %s", text(t, res))
	}
}

func TestLongRunningReturnsJobThenJobsGet(t *testing.T) {
	f := setup(t, nil)
	res := f.call(t, "fake_slow", nil)
	if res.IsError {
		t.Fatal(text(t, res))
	}
	var job map[string]any
	if err := json.Unmarshal([]byte(text(t, res)), &job); err != nil {
		t.Fatal(err)
	}
	id, _ := job["id"].(string)
	if id == "" || job["action"] != "fake.slow" || job["actor"] != "mcp" {
		t.Fatalf("job = %v", job)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := f.call(t, "jobs_get", map[string]any{"id": id})
		if res.IsError {
			t.Fatal(text(t, res))
		}
		var j map[string]any
		_ = json.Unmarshal([]byte(text(t, res)), &j)
		if j["status"] == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished: %v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	res = f.call(t, "jobs_list", map[string]any{"limit": 5})
	var list []map[string]any
	if err := json.Unmarshal([]byte(text(t, res)), &list); err != nil || len(list) != 1 {
		t.Errorf("list = %v err=%v", list, err)
	}
	res = f.call(t, "jobs_get", map[string]any{"id": "nope"})
	if !res.IsError || !strings.HasPrefix(text(t, res), "not_found: ") {
		t.Errorf("missing job: %q", text(t, res))
	}
	res = f.call(t, "jobs_cancel", map[string]any{"id": id})
	if !res.IsError || !strings.HasPrefix(text(t, res), "conflict: ") {
		t.Errorf("cancel finished: %q", text(t, res))
	}
}
