package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/llm"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/llmsession"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func newEnv() action.Env {
	servers := []config.LLMServerConfig{
		{Name: "server-a", BaseURL: "http://a.test/v1"},
		{Name: "server-b", BaseURL: "http://b.test/v1"},
	}
	list := func(_ context.Context, s config.LLMServerConfig) ([]string, error) {
		if s.Name == "server-b" {
			return nil, errors.New("server-b unreachable")
		}
		return []string{"m1", "m2"}, nil
	}
	return action.Env{LLM: llmsession.New(servers, list), Mode: action.ModeLocal, Actor: "user:admin"}
}

func find(t *testing.T, name string) action.Action {
	t.Helper()
	for _, a := range llm.Actions() {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no action %q", name)
	return action.Action{}
}

func run(t *testing.T, env action.Env, name, params string) (action.Result, error) {
	t.Helper()
	a := find(t, name)
	p, err := action.Decode(a, json.RawMessage(params))
	if err != nil {
		return action.Result{}, err
	}
	return a.Run(context.Background(), env, p)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func object(t *testing.T, res action.Result) map[string]any {
	t.Helper()
	obj, ok := res.Object.(map[string]any)
	if !ok {
		t.Fatalf("object = %#v", res.Object)
	}
	return obj
}

func selection(t *testing.T, res action.Result) map[string]string {
	t.Helper()
	obj, ok := res.Object.(map[string]any)
	if !ok {
		t.Fatalf("object = %#v", res.Object)
	}
	sel, _ := obj["selection"].(map[string]string)
	return sel
}

func TestMetadata(t *testing.T) {
	as := llm.Actions()
	if len(as) != 4 {
		t.Fatalf("got %d actions, want 4", len(as))
	}
	var reg action.Registry
	reg.Add(as...)
	kinds := map[string]action.Kind{"llm.status": action.KindRead, "llm.models": action.KindRead, "llm.select": action.KindWrite, "llm.clear": action.KindWrite}
	for _, a := range as {
		if a.Group != "llm" || a.Kind != kinds[a.Name] || len(a.Modes) != 1 || a.Modes[0] != action.ModeLocal || a.UsesLLM || a.Title == "" || a.Description == "" {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
	}
}

func TestStatusAndSelectClear(t *testing.T) {
	env := newEnv()
	res, err := run(t, env, "llm.status", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	obj := object(t, res)
	servers, _ := obj["servers"].([]map[string]string)
	if res.Render != "object" || len(servers) != 2 || servers[0]["name"] != "server-a" || servers[0]["base_url"] != "http://a.test/v1" {
		t.Errorf("status = %#v", res)
	}
	if obj["selection"] != nil {
		t.Errorf("selection = %#v, want nil", obj["selection"])
	}

	res, err = run(t, env, "llm.select", `{"server":"server-a","model":"m2"}`)
	if err != nil {
		t.Fatal(err)
	}
	if sel := selection(t, res); sel["server"] != "server-a" || sel["model"] != "m2" {
		t.Errorf("select result = %#v", res.Object)
	}
	res, _ = run(t, env, "llm.status", `{}`)
	if sel := selection(t, res); sel["server"] != "server-a" || sel["model"] != "m2" {
		t.Errorf("status after select = %#v", res.Object)
	}

	res, err = run(t, env, "llm.clear", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if object(t, res)["selection"] != nil {
		t.Errorf("clear result = %#v", res.Object)
	}
	if _, ok := env.LLM.Current(); ok {
		t.Error("selection survived clear")
	}
}

func TestModels(t *testing.T) {
	env := newEnv()
	res, err := run(t, env, "llm.models", `{"server":"server-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	obj := object(t, res)
	models, _ := obj["models"].([]string)
	if res.Render != "object" || obj["server"] != "server-a" || len(models) != 2 {
		t.Errorf("models = %#v", res)
	}
	for _, s := range []string{"server-b", "nope"} {
		_, err = run(t, env, "llm.models", `{"server":"`+s+`"}`)
		wantCode(t, err, action.CodeInvalidParams)
	}
	_, err = run(t, env, "llm.models", `{}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestSelectErrors(t *testing.T) {
	env := newEnv()
	for _, p := range []string{
		`{"server":"server-a","model":""}`,
		`{"server":"server-a","model":"nope"}`,
		`{"server":"nope","model":"m1"}`,
		`{"server":"server-b","model":"m1"}`,
		`{"server":"server-a"}`,
	} {
		_, err := run(t, env, "llm.select", p)
		wantCode(t, err, action.CodeInvalidParams)
	}
	if _, ok := env.LLM.Current(); ok {
		t.Error("failed select changed the selection")
	}
}

func TestNoServersConfigured(t *testing.T) {
	env := action.Env{Mode: action.ModeLocal}
	for name, p := range map[string]string{"llm.status": `{}`, "llm.models": `{"server":"x"}`, "llm.select": `{"server":"x","model":"y"}`, "llm.clear": `{}`} {
		_, err := run(t, env, name, p)
		wantCode(t, err, action.CodeInvalidParams)
	}
}

func TestSelectAudited(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := jobs.New(ctx, st, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); r.Wait(); _ = st.Close() })

	env := newEnv()
	a := find(t, "llm.select")
	raw := json.RawMessage(`{"server":"server-a","model":"m1"}`)
	p, err := action.Decode(a, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunSync(ctx, a, env, p, raw); err != nil {
		t.Fatal(err)
	}
	entries, err := st.ListAudit(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit = %+v err=%v", entries, err)
	}
	if e := entries[0]; e.Action != "llm.select" || e.Actor != "user:admin" || e.Outcome != "succeeded" || string(e.Params) != string(raw) {
		t.Errorf("entry = %+v", e)
	}
}
