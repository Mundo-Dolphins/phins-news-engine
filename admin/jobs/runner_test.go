package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

var t0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

func now() time.Time { return t0 }

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newRunner(t *testing.T) (*jobs.Runner, *store.Store) {
	t.Helper()
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	r, err := jobs.New(ctx, s, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { cancel(); r.Wait() })
	return r, s
}

func act(kind action.Kind, run func(context.Context, action.Env, any) (action.Result, error)) action.Action {
	return action.Action{Name: "test.act", Kind: kind, Modes: []action.Mode{action.ModeLocal}, Run: run}
}

func env() action.Env { return action.Env{Actor: "user:admin"} }

func getJob(t *testing.T, s *store.Store, id string) store.Job {
	t.Helper()
	j, ok, err := s.GetJob(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s) ok=%v err=%v", id, ok, err)
	}
	return j
}

func TestStartSucceedsAndStoresResult(t *testing.T) {
	r, s := newRunner(t)
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) {
		return action.Result{Render: "text", Text: "done"}, nil
	})
	j, err := r.Start(a, env(), nil, json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(j.ID) != 32 || j.Action != "test.act" || j.Actor != "user:admin" {
		t.Errorf("job = %+v", j)
	}
	r.Wait()
	got := getJob(t, s, j.ID)
	if got.Status != "succeeded" || got.Result == nil || got.Result.Text != "done" ||
		got.StartedAt == nil || got.FinishedAt == nil || string(got.Params) != `{"x":1}` {
		t.Errorf("stored job = %+v", got)
	}
}

func TestStartCapturesLogLines(t *testing.T) {
	r, s := newRunner(t)
	a := act(action.KindRead, func(_ context.Context, e action.Env, _ any) (action.Result, error) {
		e.Log.Info("hello", "n", 3, "who", "a b")
		e.Log.Warn("plain")
		return action.Result{}, nil
	})
	j, _ := r.Start(a, env(), nil, nil)
	r.Wait()
	logs, err := s.Logs(context.Background(), j.ID, 0)
	if err != nil || len(logs) != 2 {
		t.Fatalf("logs = %+v err=%v", logs, err)
	}
	if logs[0].Line != `hello n=3 who="a b"` || logs[1].Line != "plain" {
		t.Errorf("lines = %q, %q", logs[0].Line, logs[1].Line)
	}
}

func TestSubscribeReceivesLiveLinesAndCloses(t *testing.T) {
	r, _ := newRunner(t)
	release := make(chan struct{})
	started := make(chan struct{})
	a := act(action.KindRead, func(_ context.Context, e action.Env, _ any) (action.Result, error) {
		close(started)
		<-release
		e.Log.Info("late")
		return action.Result{}, nil
	})
	j, _ := r.Start(a, env(), nil, nil)
	<-started
	ch, unsub := r.Subscribe(j.ID)
	defer unsub()
	close(release)
	var lines []string
	for l := range ch {
		lines = append(lines, l.Line)
	}
	if len(lines) != 1 || lines[0] != "late" {
		t.Errorf("lines = %v", lines)
	}
	r.Wait()
	ch2, unsub2 := r.Subscribe(j.ID)
	defer unsub2()
	if _, open := <-ch2; open {
		t.Error("subscribe on finished job should be closed")
	}
	ch3, unsub3 := r.Subscribe("unknown")
	defer unsub3()
	if _, open := <-ch3; open {
		t.Error("subscribe on unknown job should be closed")
	}
}

func TestExclusiveSecondStartIsBusy(t *testing.T) {
	r, _ := newRunner(t)
	release := make(chan struct{})
	started := make(chan struct{})
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) {
		close(started)
		<-release
		return action.Result{}, nil
	})
	a.Exclusive = true
	if _, err := r.Start(a, env(), nil, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := r.Start(a, env(), nil, nil); !errors.Is(err, jobs.ErrBusy) {
		t.Errorf("second Start err = %v, want ErrBusy", err)
	}
	if _, err := r.RunSync(context.Background(), a, env(), nil, nil); !errors.Is(err, jobs.ErrBusy) {
		t.Errorf("RunSync err = %v, want ErrBusy", err)
	}
	var ae *action.Error
	if !errors.As(jobs.ErrBusy, &ae) || ae.Code != action.CodeBusy {
		t.Errorf("ErrBusy = %#v", jobs.ErrBusy)
	}
	// a non-exclusive action is unaffected
	ok := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil })
	if _, err := r.RunSync(context.Background(), ok, env(), nil, nil); err != nil {
		t.Errorf("non-exclusive RunSync: %v", err)
	}
	close(release)
	r.Wait()
	if _, err := r.Start(a, env(), nil, nil); err != nil {
		t.Errorf("Start after release: %v", err)
	}
}

func TestExclusiveRunSyncHoldsSlot(t *testing.T) {
	r, _ := newRunner(t)
	release := make(chan struct{})
	started := make(chan struct{})
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) {
		close(started)
		<-release
		return action.Result{}, nil
	})
	a.Exclusive = true
	done := make(chan error)
	go func() { _, err := r.RunSync(context.Background(), a, env(), nil, nil); done <- err }()
	<-started
	if _, err := r.Start(a, env(), nil, nil); !errors.Is(err, jobs.ErrBusy) {
		t.Errorf("Start during sync err = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(a, env(), nil, nil); err != nil {
		t.Errorf("Start after sync: %v", err)
	}
}

func TestCancelMarksCancelled(t *testing.T) {
	r, s := newRunner(t)
	started := make(chan struct{})
	a := act(action.KindRead, func(ctx context.Context, _ action.Env, _ any) (action.Result, error) {
		close(started)
		<-ctx.Done()
		return action.Result{}, ctx.Err()
	})
	a.Exclusive = true
	j, _ := r.Start(a, env(), nil, nil)
	<-started
	if err := r.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if got := getJob(t, s, j.ID); got.Status != "cancelled" || got.FinishedAt == nil {
		t.Errorf("job = %+v", got)
	}
	if _, err := r.Start(a, env(), nil, nil); err != nil {
		t.Errorf("slot not freed after cancel: %v", err)
	}
	r.Wait()
}

func TestCancelErrors(t *testing.T) {
	r, _ := newRunner(t)
	var ae *action.Error
	if err := r.Cancel("nope"); !errors.As(err, &ae) || ae.Code != action.CodeNotFound {
		t.Errorf("unknown: %v", err)
	}
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil })
	j, _ := r.Start(a, env(), nil, nil)
	r.Wait()
	if err := r.Cancel(j.ID); !errors.As(err, &ae) || ae.Code != action.CodeConflict {
		t.Errorf("finished: %v", err)
	}
}

func TestBaseContextCancelStopsJobs(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := jobs.New(ctx, s, now)
	started := make(chan struct{})
	a := act(action.KindRead, func(ctx context.Context, _ action.Env, _ any) (action.Result, error) {
		close(started)
		<-ctx.Done()
		return action.Result{}, ctx.Err()
	})
	j, _ := r.Start(a, env(), nil, nil)
	<-started
	cancel()
	r.Wait()
	if got := getJob(t, s, j.ID); got.Status != "cancelled" {
		t.Errorf("status = %s", got.Status)
	}
}

func TestPanicBecomesFailed(t *testing.T) {
	r, s := newRunner(t)
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { panic("boom") })
	j, _ := r.Start(a, env(), nil, nil)
	r.Wait()
	got := getJob(t, s, j.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "boom") {
		t.Errorf("job = %+v", got)
	}
	if _, err := r.RunSync(context.Background(), a, env(), nil, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("sync panic err = %v", err)
	}
}

func TestFailedStoresError(t *testing.T) {
	r, s := newRunner(t)
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) {
		return action.Result{}, errors.New("nope")
	})
	j, _ := r.Start(a, env(), nil, nil)
	r.Wait()
	if got := getJob(t, s, j.ID); got.Status != "failed" || got.Error != "nope" {
		t.Errorf("job = %+v", got)
	}
}

func TestNewMarksStaleRunningInterrupted(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateJob(ctx, store.Job{ID: "stale", Action: "x", Actor: "a", Status: "running", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	r, err := jobs.New(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := getJob(t, s, "stale"); got.Status != "interrupted" {
		t.Errorf("status = %s", got.Status)
	}
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil })
	a.Exclusive = true
	if _, err := r.Start(a, env(), nil, nil); err != nil {
		t.Errorf("exclusive Start: %v", err)
	}
	r.Wait()
}

func TestWriteActionAudited(t *testing.T) {
	r, s := newRunner(t)
	ok := func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil }
	bad := func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, errors.New("x") }
	raw := json.RawMessage(`{"id":7}`)

	if _, err := r.RunSync(context.Background(), act(action.KindWrite, ok), env(), nil, raw); err != nil {
		t.Fatal(err)
	}
	_, _ = r.RunSync(context.Background(), act(action.KindDestructive, bad), env(), nil, raw)
	j, _ := r.Start(act(action.KindWrite, ok), env(), nil, raw)
	r.Wait()
	_ = j

	entries, err := s.ListAudit(context.Background(), 10)
	if err != nil || len(entries) != 3 {
		t.Fatalf("audit = %+v err=%v", entries, err)
	}
	outcomes := map[string]int{}
	for _, e := range entries {
		outcomes[e.Outcome]++
		if e.Actor != "user:admin" || e.Action != "test.act" || string(e.Params) != `{"id":7}` || !e.At.Equal(t0) {
			t.Errorf("entry = %+v", e)
		}
		if e.Outcome == "failed" && (e.Error != "x" || e.Kind != "destructive") {
			t.Errorf("failed entry = %+v", e)
		}
	}
	if outcomes["succeeded"] != 2 || outcomes["failed"] != 1 {
		t.Errorf("outcomes = %v", outcomes)
	}
}

func TestCancelledWriteAuditedCancelled(t *testing.T) {
	r, s := newRunner(t)
	started := make(chan struct{})
	a := act(action.KindWrite, func(ctx context.Context, _ action.Env, _ any) (action.Result, error) {
		close(started)
		<-ctx.Done()
		return action.Result{}, ctx.Err()
	})
	j, _ := r.Start(a, env(), nil, nil)
	<-started
	_ = r.Cancel(j.ID)
	r.Wait()
	entries, _ := s.ListAudit(context.Background(), 10)
	if len(entries) != 1 || entries[0].Outcome != "cancelled" {
		t.Errorf("audit = %+v", entries)
	}
}

func TestReadActionNotAudited(t *testing.T) {
	r, s := newRunner(t)
	a := act(action.KindRead, func(context.Context, action.Env, any) (action.Result, error) { return action.Result{}, nil })
	_, _ = r.RunSync(context.Background(), a, env(), nil, nil)
	_, _ = r.Start(a, env(), nil, nil)
	r.Wait()
	if entries, _ := s.ListAudit(context.Background(), 10); len(entries) != 0 {
		t.Errorf("audit = %+v", entries)
	}
}

func TestRunSyncCreatesNoJobAndPassesParams(t *testing.T) {
	r, s := newRunner(t)
	type p struct{ N int }
	var got any
	a := act(action.KindRead, func(_ context.Context, e action.Env, params any) (action.Result, error) {
		got = params
		if e.Log == nil || e.Now == nil {
			t.Error("env.Log/Now not defaulted")
		}
		return action.Result{Text: "sync"}, nil
	})
	res, err := r.RunSync(context.Background(), a, env(), &p{N: 4}, nil)
	gp, _ := got.(*p)
	if err != nil || res.Text != "sync" || gp == nil || gp.N != 4 {
		t.Fatalf("res=%+v err=%v got=%v", res, err, got)
	}
	if list, _ := s.ListJobs(context.Background(), 10); len(list) != 0 {
		t.Errorf("jobs = %+v", list)
	}
}
