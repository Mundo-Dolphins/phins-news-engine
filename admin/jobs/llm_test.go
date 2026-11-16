package jobs_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/llmsession"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

type llmParams struct{ BaseURL, Model string }

func (p llmParams) LLMOverride() (string, string) { return p.BaseURL, p.Model }

type fakeLLM struct {
	sess  *llmsession.Session
	calls atomic.Int32
	down  atomic.Bool
}

// newFakeLLM returns a session over server-a and server-b; server-a serves
// m1 only and fails while down is set.
func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{}
	servers := []config.LLMServerConfig{
		{Name: "server-a", BaseURL: "http://a/v1"},
		{Name: "server-b", BaseURL: "http://b/v1"},
	}
	f.sess = llmsession.New(servers, func(_ context.Context, s config.LLMServerConfig) ([]string, error) {
		f.calls.Add(1)
		if f.down.Load() {
			return nil, errors.New("connection refused")
		}
		return []string{"m1"}, nil
	})
	return f
}

func (f *fakeLLM) selectA(t *testing.T) {
	t.Helper()
	if err := f.sess.Select(context.Background(), llmservers.Selection{Server: "server-a", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	f.calls.Store(0)
}

func llmEnv(f *fakeLLM) *action.Env {
	cfg := &config.Config{}
	cfg.OfflineTraining.LLM.BaseURL = "http://yaml/v1"
	cfg.OfflineTraining.LLM.APIKey = "yaml-key"
	return &action.Env{Actor: "user:admin", Config: cfg, LLM: f.sess}
}

func wantInvalid(t *testing.T, err error) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid_params", err)
	}
}

func TestApplyLLMNoSelection(t *testing.T) {
	f := newFakeLLM(t)
	env := llmEnv(f)
	orig := env.Config
	sel, err := jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, env, nil)
	if sel != nil || err != nil || env.Config != orig || f.calls.Load() != 0 {
		t.Fatalf("sel=%v err=%v same=%v calls=%d", sel, err, env.Config == orig, f.calls.Load())
	}
	env.LLM = nil
	if sel, err := jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, env, nil); sel != nil || err != nil {
		t.Fatalf("nil session: %v %v", sel, err)
	}
}

func TestApplyLLMNonLLMActionAppliesWithoutCheck(t *testing.T) {
	f := newFakeLLM(t)
	f.selectA(t)
	f.down.Store(true)
	env := llmEnv(f)
	orig := env.Config
	sel, err := jobs.ApplyLLM(context.Background(), action.Action{}, env, nil)
	if err != nil || sel == nil {
		t.Fatalf("sel=%v err=%v", sel, err)
	}
	if env.Config == orig || env.Config.OfflineTraining.LLM.BaseURL != "http://a/v1" || f.calls.Load() != 0 {
		t.Errorf("url=%q calls=%d", env.Config.OfflineTraining.LLM.BaseURL, f.calls.Load())
	}
	if orig.OfflineTraining.LLM.BaseURL != "http://yaml/v1" {
		t.Error("original config mutated")
	}
}

func TestApplyLLMLLMAction(t *testing.T) {
	f := newFakeLLM(t)
	f.selectA(t)
	env := llmEnv(f)
	sel, err := jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, env, nil)
	if err != nil || sel == nil || *sel != (llmservers.Selection{Server: "server-a", Model: "m1"}) {
		t.Fatalf("sel=%v err=%v", sel, err)
	}
	if env.Config.OfflineTraining.LLM.BaseURL != "http://a/v1" || env.Config.OfflineTraining.LLM.Model != "m1" || f.calls.Load() != 1 {
		t.Errorf("cfg=%+v calls=%d", env.Config.OfflineTraining.LLM, f.calls.Load())
	}

	f.down.Store(true)
	_, err = jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, llmEnv(f), nil)
	wantInvalid(t, err)
}

func TestApplyLLMBaseURLOverrideSkipsSelection(t *testing.T) {
	f := newFakeLLM(t)
	f.selectA(t)
	env := llmEnv(f)
	orig := env.Config
	sel, err := jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, env, &llmParams{BaseURL: "http://other/v1"})
	if sel != nil || err != nil || env.Config != orig || f.calls.Load() != 0 {
		t.Fatalf("sel=%v err=%v same=%v calls=%d", sel, err, env.Config == orig, f.calls.Load())
	}
	if env.Config.OfflineTraining.LLM.APIKey != "yaml-key" {
		t.Errorf("api key = %q", env.Config.OfflineTraining.LLM.APIKey)
	}
	// Also for value params.
	if sel, _ := jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, env, llmParams{BaseURL: "http://other/v1"}); sel != nil || env.Config != orig {
		t.Error("value params not honoured")
	}
}

func TestApplyLLMModelOverride(t *testing.T) {
	f := newFakeLLM(t)
	f.sess = llmsession.New(f.sess.Servers(), func(_ context.Context, s config.LLMServerConfig) ([]string, error) {
		f.calls.Add(1)
		return []string{"m1", "m2"}, nil
	})
	f.selectA(t)
	env := llmEnv(f)
	sel, err := jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, env, &llmParams{Model: "m2"})
	if err != nil || sel == nil || sel.Server != "server-a" || sel.Model != "m1" {
		t.Fatalf("sel=%v err=%v", sel, err)
	}
	if env.Config.OfflineTraining.LLM.BaseURL != "http://a/v1" {
		t.Errorf("url = %q", env.Config.OfflineTraining.LLM.BaseURL)
	}

	// A model the server does not serve is invalid. The override must be what
	// is checked, not the selected model (m1 is served).
	f.sess = llmsession.New(f.sess.Servers(), func(context.Context, config.LLMServerConfig) ([]string, error) {
		return []string{"m1"}, nil
	})
	if err := f.sess.Select(context.Background(), llmservers.Selection{Server: "server-a", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	_, err = jobs.ApplyLLM(context.Background(), action.Action{UsesLLM: true}, llmEnv(f), &llmParams{Model: "m2"})
	wantInvalid(t, err)
	if err == nil || !strings.Contains(err.Error(), "m2") {
		t.Errorf("err = %v", err)
	}
}

func TestStartLLMUnavailableCreatesNoJobAndFreesSlot(t *testing.T) {
	r, s := newRunner(t)
	f := newFakeLLM(t)
	f.selectA(t)
	f.down.Store(true)
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil })
	a.UsesLLM, a.Exclusive = true, true
	_, err := r.Start(a, *llmEnv(f), nil, nil)
	wantInvalid(t, err)
	jl, err := s.ListJobs(context.Background(), 10)
	if err != nil || len(jl) != 0 {
		t.Fatalf("jobs = %+v err=%v", jl, err)
	}
	f.down.Store(false)
	if _, err := r.Start(a, *llmEnv(f), nil, nil); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	r.Wait()
}

func TestRunSyncLLMUnavailableFreesSlot(t *testing.T) {
	r, _ := newRunner(t)
	f := newFakeLLM(t)
	f.selectA(t)
	f.down.Store(true)
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil })
	a.UsesLLM, a.Exclusive = true, true
	_, err := r.RunSync(context.Background(), a, *llmEnv(f), nil, nil)
	wantInvalid(t, err)
	f.down.Store(false)
	if _, err := r.RunSync(context.Background(), a, *llmEnv(f), nil, nil); err != nil {
		t.Fatalf("second RunSync: %v", err)
	}
}

func TestStartLLMSnapshotsSelection(t *testing.T) {
	r, _ := newRunner(t)
	f := newFakeLLM(t)
	f.selectA(t)
	started, release := make(chan struct{}), make(chan struct{})
	var got atomic.Value
	a := act(action.KindRead, func(_ context.Context, e action.Env, _ any) (action.Result, error) {
		close(started)
		<-release
		got.Store(e.Config.OfflineTraining.LLM.BaseURL)
		return action.Result{}, nil
	})
	if _, err := r.Start(a, *llmEnv(f), nil, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := f.sess.Select(context.Background(), llmservers.Selection{Server: "server-b", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	r.Wait()
	if got.Load() != "http://a/v1" {
		t.Errorf("action saw %v", got.Load())
	}
}

func TestStartLLMLogsSelection(t *testing.T) {
	r, s := newRunner(t)
	f := newFakeLLM(t)
	f.selectA(t)
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil })
	j, err := r.Start(a, *llmEnv(f), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	logs, err := s.Logs(context.Background(), j.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if strings.Contains(l.Line, "server=server-a") && strings.Contains(l.Line, "model=m1") {
			return
		}
	}
	t.Fatalf("no llm log line in %+v", logs)
}
