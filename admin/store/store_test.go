package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

var t0 = time.Date(2026, 11, 5, 10, 0, 0, 123, time.UTC)

func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestJobLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	j := store.Job{ID: "j1", Action: "claims.list", Actor: "admin", Status: "queued", CreatedAt: t0}
	if err := s.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetJob(ctx, "j1")
	if err != nil || !ok {
		t.Fatalf("GetJob ok=%v err=%v", ok, err)
	}
	if got.Result != nil || got.StartedAt != nil || string(got.Params) != "{}" || !got.CreatedAt.Equal(t0) {
		t.Errorf("unexpected initial job %+v", got)
	}
	if _, ok, _ := s.GetJob(ctx, "nope"); ok {
		t.Error("missing job reported found")
	}

	start, end := t0.Add(time.Second), t0.Add(2*time.Second)
	j.Status, j.StartedAt, j.FinishedAt = "succeeded", &start, &end
	j.Params = json.RawMessage(`{"a":1}`)
	j.Result = &action.Result{Render: "text", Text: "ok", Columns: []action.Column{{Key: "k", Label: "K"}}}
	if err := s.UpdateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetJob(ctx, "j1")
	if got.Status != "succeeded" || got.StartedAt == nil || !got.StartedAt.Equal(start) ||
		got.StartedAt.Location() != time.UTC || !got.FinishedAt.Equal(end) {
		t.Errorf("update not persisted: %+v", got)
	}
	if got.Result == nil || !reflect.DeepEqual(*got.Result, *j.Result) || string(got.Params) != `{"a":1}` {
		t.Errorf("result/params = %+v %s", got.Result, got.Params)
	}
	if err := s.UpdateJob(ctx, store.Job{ID: "ghost"}); err == nil {
		t.Error("UpdateJob on missing job should error")
	}
}

func TestListJobsNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i, id := range []string{"a", "b", "c"} {
		if err := s.CreateJob(ctx, store.Job{ID: id, Status: "queued", CreatedAt: t0.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	// Whole-second vs fractional timestamps must still order chronologically.
	_ = s.CreateJob(ctx, store.Job{ID: "w", Status: "queued", CreatedAt: time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)})
	_ = s.CreateJob(ctx, store.Job{ID: "f", Status: "queued", CreatedAt: time.Date(2026, 11, 5, 11, 0, 0, 500_000_000, time.UTC)})
	if top, _ := s.ListJobs(ctx, 1); len(top) != 1 || top[0].ID != "f" {
		t.Fatalf("fractional ordering wrong: %+v", top)
	}
	got, err := s.ListJobs(ctx, 10)
	if err != nil || len(got) != 5 || got[0].ID != "f" || got[1].ID != "w" || got[2].ID != "c" {
		t.Errorf("ListJobs = %+v err=%v", got, err)
	}
}

func TestLogsPaging(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.CreateJob(ctx, store.Job{ID: "j", Status: "running", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for _, l := range []string{"one", "two", "three"} {
		seq, err := s.AppendLog(ctx, "j", l, t0)
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	if seqs[0] >= seqs[1] || seqs[1] >= seqs[2] {
		t.Fatalf("seqs not ascending: %v", seqs)
	}
	all, _ := s.Logs(ctx, "j", 0)
	if len(all) != 3 || all[0].Line != "one" || !all[0].At.Equal(t0) {
		t.Errorf("all = %+v", all)
	}
	rest, _ := s.Logs(ctx, "j", seqs[0])
	if len(rest) != 2 || rest[0].Line != "two" || rest[1].Line != "three" {
		t.Errorf("rest = %+v", rest)
	}
	none, _ := s.Logs(ctx, "j", seqs[2])
	if len(none) != 0 {
		t.Errorf("none = %+v", none)
	}
}

func TestMarkRunningInterrupted(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, st := range []string{"queued", "running", "succeeded", "failed", "cancelled"} {
		if err := s.CreateJob(ctx, store.Job{ID: st, Status: st, CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(time.Hour)
	n, err := s.MarkRunningInterrupted(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for st, want := range map[string]string{"queued": "interrupted", "running": "interrupted",
		"succeeded": "succeeded", "failed": "failed", "cancelled": "cancelled"} {
		j, _, _ := s.GetJob(ctx, st)
		if j.Status != want {
			t.Errorf("%s -> %s, want %s", st, j.Status, want)
		}
	}
	j, _, _ := s.GetJob(ctx, "running")
	if j.FinishedAt == nil || !j.FinishedAt.Equal(now) {
		t.Errorf("FinishedAt = %v", j.FinishedAt)
	}
	j, _, _ = s.GetJob(ctx, "succeeded")
	if j.FinishedAt != nil {
		t.Error("terminal job touched")
	}
}

func TestAuditNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i, a := range []string{"first", "second", "third"} {
		e := store.AuditEntry{At: t0.Add(time.Duration(i) * time.Second), Actor: "admin", Action: a, Kind: "write", Outcome: "ok"}
		if i == 0 {
			e.Params = json.RawMessage(`{"x":1}`)
		}
		if err := s.AddAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListAudit(ctx, 2)
	if err != nil || len(got) != 2 || got[0].Action != "third" || got[1].Action != "second" || got[0].ID == 0 {
		t.Fatalf("ListAudit = %+v err=%v", got, err)
	}
	all, _ := s.ListAudit(ctx, 10)
	if string(all[2].Params) != `{"x":1}` || string(all[0].Params) != "{}" {
		t.Errorf("params = %s / %s", all[2].Params, all[0].Params)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.CreateSession(ctx, "h1", "admin", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "h2", "admin", t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	user, ok, err := s.TouchSession(ctx, "h1", t0, t0.Add(12*time.Hour))
	if err != nil || !ok || user != "admin" {
		t.Fatalf("touch h1 = %q %v %v", user, ok, err)
	}
	// Extended: still valid 6h later.
	if _, ok, _ := s.TouchSession(ctx, "h1", t0.Add(6*time.Hour), t0.Add(18*time.Hour)); !ok {
		t.Error("expiry was not extended")
	}
	if _, ok, _ := s.TouchSession(ctx, "h2", t0, t0.Add(time.Hour)); ok {
		t.Error("expired session returned")
	}
	if _, ok, _ := s.TouchSession(ctx, "missing", t0, t0.Add(time.Hour)); ok {
		t.Error("missing session returned")
	}
	if err := s.PurgeExpiredSessions(ctx, t0); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TouchSession(ctx, "h2", t0.Add(-2*time.Hour), t0.Add(-time.Hour)); ok {
		t.Error("expired session survived purge")
	}
	if err := s.DeleteSession(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TouchSession(ctx, "h1", t0, t0.Add(time.Hour)); ok {
		t.Error("deleted session returned")
	}
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "admin.db")
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.CreateJob(ctx, store.Job{ID: "j", Status: "queued", CreatedAt: t0})
	_ = s.Close()
	s, err = store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if _, ok, _ := s.GetJob(ctx, "j"); !ok {
		t.Error("job lost on reopen")
	}
}
