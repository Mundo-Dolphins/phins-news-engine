package usecase_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// fakeTaskRunner runs a function and records what it was given.
type fakeTaskRunner struct {
	kind    string
	run     func(ctx context.Context, tc out.TaskContext, t out.Task, signals []out.TaskSignal) (out.TaskOutcome, error)
	calls   int
	gotTC   out.TaskContext
	gotSigs []out.TaskSignal
}

func (r *fakeTaskRunner) Kind() string { return r.kind }
func (r *fakeTaskRunner) Run(ctx context.Context, tc out.TaskContext, t out.Task, signals []out.TaskSignal) (out.TaskOutcome, error) {
	r.calls++
	r.gotTC, r.gotSigs = tc, signals
	return r.run(ctx, tc, t, signals)
}

type warnLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *warnLogger) Debug(string, ...any)        {}
func (l *warnLogger) Info(string, ...any)         {}
func (l *warnLogger) Error(string, error, ...any) {}
func (l *warnLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

type taskFixture struct {
	timedFixture
	tasks  *sqlite.TaskRepository
	taskID int64
}

func newTaskFixture(t *testing.T) *taskFixture {
	t.Helper()
	f := &taskFixture{timedFixture: *newTimedFixture(t)}
	f.tasks = sqlite.NewTaskRepository(f.db)
	// The outbox row needs its article.
	a := eventTestArticle(t, "art-1", "src-a", "Some story", "https://example.com/art-1")
	a.Status = article.StatusPendingPublication
	a.FetchedAt = f.clock.Now()
	if err := sqlite.NewArticleRepository(f.db).Save(context.Background(), a); err != nil {
		t.Fatalf("save article: %v", err)
	}
	if _, err := f.tasks.Enqueue(context.Background(), "poll", "k1", "art-1", json.RawMessage(`{"n":0}`), f.clock.Now()); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	due, err := f.tasks.Due(context.Background(), f.clock.Now(), []string{"poll"}, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("Due = %+v, %v", due, err)
	}
	f.taskID = due[0].ID
	return f
}

func (f *taskFixture) loop(t *testing.T, enabled bool, timeout time.Duration, logger out.Logger, runners ...out.TaskRunner) *usecase.TaskRunnerLoop {
	t.Helper()
	settings := map[string]usecase.EventHandlerSettings{}
	for _, r := range runners {
		settings[r.Kind()] = usecase.EventHandlerSettings{Enabled: enabled, Config: map[string]any{"who": r.Kind()}}
	}
	if logger == nil {
		logger = fakeLogger{}
	}
	l, err := usecase.NewTaskRunner(runners, settings,
		usecase.EventDeps{Clock: f.clock, Logger: logger, Alerts: f.alerts},
		f.tasks, sqlite.NewOutboxRepository(f.db), sqlite.NewUnitOfWork(f.db), timeout, 5)
	if err != nil {
		t.Fatalf("NewTaskRunner() error = %v", err)
	}
	return l
}

func (f *taskFixture) get(t *testing.T) out.Task {
	t.Helper()
	got, ok, err := f.tasks.Get(context.Background(), f.taskID)
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	return got
}

func okOutcome(status out.TaskStatus, runAfter time.Time, actions ...out.EventAction) out.TaskOutcome {
	return out.TaskOutcome{Status: status, State: json.RawMessage(`{"n":1}`), RunAfter: runAfter, Actions: actions}
}

func publication() out.EnqueuePublication {
	return out.EnqueuePublication{
		Destination: publishing.DestinationBluesky,
		Payload:     publishing.MessagePayload{Text: "Poll result", Kind: "poll"},
	}
}

func failing(err string) func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
	return func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		panic(err)
	}
}

func TestTaskRunnerPersistsOutcomeAndPublication(t *testing.T) {
	f := newTaskFixture(t)
	if err := f.tasks.AddSignal(context.Background(), f.taskID, json.RawMessage(`{"s":1}`), f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	wake := f.clock.Now().Add(time.Hour)
	r := &fakeTaskRunner{kind: "poll", run: func(_ context.Context, _ out.TaskContext, _ out.Task, sigs []out.TaskSignal) (out.TaskOutcome, error) {
		o := okOutcome(out.TaskWaiting, wake, publication())
		o.Consumed = []int64{sigs[0].ID}
		return o, nil
	}}
	res, err := f.loop(t, true, time.Second, nil, r).Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Ran != 1 || res.Errors != 0 || res.Enqueued != 1 {
		t.Fatalf("result = %+v", res)
	}
	got := f.get(t)
	if got.Status != out.TaskWaiting || string(got.State) != `{"n":1}` || !got.RunAfter.Equal(wake) || got.Attempts != 0 {
		t.Fatalf("task = %+v", got)
	}
	if n := destinationOutboxCount(t, f.db, "art-1", "bluesky"); n != 1 {
		t.Fatalf("outbox rows for the task's article = %d, want 1", n)
	}
	if left, _ := f.tasks.Signals(context.Background(), f.taskID); len(left) != 0 {
		t.Fatalf("signals left = %+v, want consumed", left)
	}
	if len(r.gotSigs) != 1 || string(r.gotSigs[0].Payload) != `{"s":1}` {
		t.Fatalf("runner got signals %+v", r.gotSigs)
	}
}

func TestTaskRunnerErrorBacksOffAndKeepsSignals(t *testing.T) {
	f := newTaskFixture(t)
	if err := f.tasks.AddSignal(context.Background(), f.taskID, nil, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return out.TaskOutcome{}, context.Canceled
	}}
	res, err := f.loop(t, true, time.Second, nil, r).Run(context.Background())
	if err != nil || res.Ran != 1 || res.Errors != 1 {
		t.Fatalf("Run() = %+v, %v", res, err)
	}
	got := f.get(t)
	if got.Attempts != 1 || !got.RunAfter.Equal(f.clock.Now().Add(30*time.Second)) {
		t.Fatalf("task = %+v, want attempts 1 and run_after now+30s", got)
	}
	if string(got.State) != `{"n":0}` || got.Status != out.TaskPending {
		t.Fatalf("state/status touched: %+v", got)
	}
	if left, _ := f.tasks.Signals(context.Background(), f.taskID); len(left) != 1 {
		t.Fatalf("signals = %+v, want 1 intact", left)
	}
}

func TestTaskRunnerBackoffGrowsAndIsCapped(t *testing.T) {
	f := newTaskFixture(t)
	r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return out.TaskOutcome{}, context.Canceled
	}}
	l := f.loop(t, true, time.Second, nil, r)
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute}
	for i, d := range want {
		if _, err := l.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := f.get(t)
		if got.Attempts != i+1 || !got.RunAfter.Equal(f.clock.Now().Add(d)) {
			t.Fatalf("after failure %d: %+v, want run_after now+%s", i+1, got, d)
		}
		f.clock.Set(got.RunAfter)
	}
	// Many failures later the delay stops at TaskMaxBackoff.
	task := f.get(t)
	task.Attempts = 40
	if err := f.tasks.Save(context.Background(), task, nil, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t); !got.RunAfter.Equal(f.clock.Now().Add(usecase.TaskMaxBackoff)) {
		t.Fatalf("run_after = %s, want now+%s", got.RunAfter, usecase.TaskMaxBackoff)
	}
}

func TestTaskRunnerPanicAndTimeoutAreErrors(t *testing.T) {
	f := newTaskFixture(t)
	r := &fakeTaskRunner{kind: "poll", run: failing("boom")}
	l := f.loop(t, true, 50*time.Millisecond, nil, r)
	res, err := l.Run(context.Background())
	if err != nil || res.Panics != 1 || res.Errors != 0 {
		t.Fatalf("panic run = %+v, %v", res, err)
	}
	if got := f.get(t); got.Attempts != 1 || string(got.State) != `{"n":0}` {
		t.Fatalf("after panic: %+v", got)
	}
	f.clock.Set(f.clock.Now().Add(time.Minute))

	r.run = func(ctx context.Context, _ out.TaskContext, _ out.Task, _ []out.TaskSignal) (out.TaskOutcome, error) {
		<-ctx.Done()
		// A late answer must not be saved.
		return okOutcome(out.TaskDone, time.Time{}, publication()), nil
	}
	res, err = l.Run(context.Background())
	if err != nil || res.Timeouts != 1 || res.Enqueued != 0 {
		t.Fatalf("timeout run = %+v, %v", res, err)
	}
	if got := f.get(t); got.Attempts != 2 || got.Status != out.TaskPending {
		t.Fatalf("after timeout: %+v", got)
	}
	if n := destinationOutboxCount(t, f.db, "art-1", "bluesky"); n != 0 {
		t.Fatalf("outbox rows = %d, want none", n)
	}
}

func TestTaskRunnerAlertsOnThirdFailure(t *testing.T) {
	f := newTaskFixture(t)
	r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return out.TaskOutcome{}, context.Canceled
	}}
	l := f.loop(t, true, time.Second, nil, r)
	for i := 0; i < 3; i++ {
		if _, err := l.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if want := map[int]int{0: 0, 1: 0, 2: 1}[i]; f.alerts.count() != want {
			t.Fatalf("after failure %d: %d alerts, want %d", i+1, f.alerts.count(), want)
		}
		f.clock.Set(f.get(t).RunAfter)
	}
	if !strings.HasPrefix(f.alerts.messages[0], "poll: ") {
		t.Fatalf("alert = %q, want attributed to the kind", f.alerts.messages[0])
	}
}

func TestTaskRunnerRejectsInvalidOutcome(t *testing.T) {
	cases := map[string]out.TaskOutcome{
		"waiting without run_after": okOutcome(out.TaskWaiting, time.Time{}),
		"unknown status":            okOutcome("paused", time.Now()),
		"invalid state":             {Status: out.TaskDone, State: json.RawMessage(`{`)},
		"foreign signal":            {Status: out.TaskDone, Consumed: []int64{9999}},
		"unknown action":            {Status: out.TaskDone, Actions: []out.EventAction{out.EnqueueTask{Kind: "x", Key: "y"}}},
		"invalid payload":           {Status: out.TaskDone, Actions: []out.EventAction{out.EnqueuePublication{Destination: publishing.DestinationBluesky, Payload: publishing.MessagePayload{Text: "x", Link: "nope"}}}},
	}
	for name, outcome := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTaskFixture(t)
			r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
				return outcome, nil
			}}
			res, err := f.loop(t, true, time.Second, nil, r).Run(context.Background())
			if err != nil || res.Errors != 1 || res.Enqueued != 0 {
				t.Fatalf("Run() = %+v, %v", res, err)
			}
			got := f.get(t)
			if got.Status != out.TaskPending || string(got.State) != `{"n":0}` || got.Attempts != 1 {
				t.Fatalf("task = %+v, want untouched apart from the backoff", got)
			}
		})
	}
}

func TestTaskRunnerSkipsDisabledKind(t *testing.T) {
	f := newTaskFixture(t)
	r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return okOutcome(out.TaskDone, time.Time{}), nil
	}}
	l := f.loop(t, false, time.Second, nil, r)
	for i := 0; i < 2; i++ {
		res, err := l.Run(context.Background())
		if err != nil || res.Ran != 0 {
			t.Fatalf("Run() = %+v, %v", res, err)
		}
	}
	if r.calls != 0 || f.get(t).Status != out.TaskPending {
		t.Fatalf("disabled runner ran: calls=%d", r.calls)
	}
}

func TestTaskRunnerSharesConfigWithEventHandler(t *testing.T) {
	f := newTaskFixture(t)
	logger := &warnLogger{}
	var handlerCfg map[string]any
	h := &fakeEventHandler{name: "poll", match: "Some", handle: func(_ context.Context, rc out.EventContext, _ article.Article) (out.EventResult, error) {
		handlerCfg = rc.Config
		return out.NoEventMatch(), nil
	}}
	settings := map[string]usecase.EventHandlerSettings{"poll": {Enabled: true, Config: map[string]any{"channel": "c1"}}}
	deps := usecase.EventDeps{Clock: f.clock, Logger: logger, Alerts: f.alerts}
	d, err := usecase.NewEventDispatcher([]out.EventHandler{h}, settings, deps, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return okOutcome(out.TaskDone, time.Time{}), nil
	}}
	l, err := usecase.NewTaskRunner([]out.TaskRunner{r}, settings, deps, f.tasks, sqlite.NewOutboxRepository(f.db), sqlite.NewUnitOfWork(f.db), time.Second, 5)
	if err != nil {
		t.Fatal(err)
	}
	var metrics in.EventHandlerMetrics
	d.Dispatch(context.Background(), eventTestArticle(t, "a2", "src-a", "Some title", "https://example.com/a2"), &metrics)
	if _, err := l.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if handlerCfg["channel"] != "c1" || r.gotTC.Config["channel"] != "c1" {
		t.Fatalf("handler config = %v, runner config = %v, want both events.poll.config", handlerCfg, r.gotTC.Config)
	}
	if len(logger.warns) != 0 {
		t.Fatalf("warnings = %v, want none", logger.warns)
	}
}

func TestTaskRunnerRejectsDuplicateKind(t *testing.T) {
	f := newTaskFixture(t)
	run := func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return out.TaskOutcome{}, nil
	}
	_, err := usecase.NewTaskRunner(
		[]out.TaskRunner{&fakeTaskRunner{kind: "poll", run: run}, &fakeTaskRunner{kind: " poll ", run: run}},
		nil, usecase.EventDeps{Clock: f.clock, Logger: fakeLogger{}}, f.tasks, nil, nil, time.Second, 5,
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want duplicate kind", err)
	}
	if _, err := usecase.NewTaskRunner([]out.TaskRunner{&fakeTaskRunner{kind: "", run: run}}, nil,
		usecase.EventDeps{Clock: f.clock, Logger: fakeLogger{}}, f.tasks, nil, nil, time.Second, 5); err == nil {
		t.Fatal("blank kind accepted")
	}
}

func TestTaskRunnerDisabledKindsDoNotStarveEnabledOnes(t *testing.T) {
	f := newTaskFixture(t) // one due "poll" task, created last
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		// Older run_after than the poll task, so they come first in the queue.
		if _, err := f.tasks.Enqueue(ctx, "idle", string(rune('a'+i)), "", nil, f.clock.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return okOutcome(out.TaskDone, time.Time{}), nil
	}}
	idle := &fakeTaskRunner{kind: "idle", run: r.run}
	settings := map[string]usecase.EventHandlerSettings{"poll": {Enabled: true}, "idle": {Enabled: false}}
	l, err := usecase.NewTaskRunner([]out.TaskRunner{r, idle}, settings,
		usecase.EventDeps{Clock: f.clock, Logger: fakeLogger{}}, f.tasks, sqlite.NewOutboxRepository(f.db), sqlite.NewUnitOfWork(f.db), time.Second, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || idle.calls != 0 || f.get(t).Status != out.TaskDone {
		t.Fatalf("poll calls=%d idle calls=%d status=%s", r.calls, idle.calls, f.get(t).Status)
	}
}

func TestTaskRunnerSignalledTaskHonoursBackoffAfterFailure(t *testing.T) {
	f := newTaskFixture(t)
	if err := f.tasks.AddSignal(context.Background(), f.taskID, nil, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	r := &fakeTaskRunner{kind: "poll", run: func(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
		return out.TaskOutcome{}, context.Canceled
	}}
	l := f.loop(t, true, time.Second, nil, r)
	for i := 0; i < 2; i++ {
		if _, err := l.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r.calls != 1 || f.get(t).Attempts != 1 {
		t.Fatalf("calls=%d attempts=%d; the backoff must hold despite the signal", r.calls, f.get(t).Attempts)
	}
	f.clock.Set(f.get(t).RunAfter)
	if _, err := l.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.calls != 2 {
		t.Fatalf("calls=%d, want a retry once RunAfter passed", r.calls)
	}
}
