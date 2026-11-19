package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// DefaultTaskTimeout bounds one TaskRunner.Run call when none is configured.
	DefaultTaskTimeout = 10 * time.Minute
	// DefaultTaskInterval is the task loop tick when none is configured.
	DefaultTaskInterval = 30 * time.Second
	// DefaultTaskBatch is how many due tasks one pass takes.
	DefaultTaskBatch = 5
	// TaskMaxBackoff caps the delay after consecutive failures.
	TaskMaxBackoff = time.Hour

	taskBaseBackoff = 30 * time.Second
	// taskAlertAttempts is the consecutive failure count that raises an alert.
	taskAlertAttempts = 3
)

// TaskRunnerLoop advances the background tasks (see out.Task). On each Run it
// takes the due tasks and hands each to the runner of its kind. An error,
// panic or timeout in one runner backs that task off and never reaches the
// pipeline or another task. Tasks take no event claims: UNIQUE (kind, key)
// already keeps them unique.
type TaskRunnerLoop struct {
	runners map[string]registeredTaskRunner
	deps    EventDeps
	tasks   out.TaskRepository
	outbox  out.DestinationOutboxRepository
	uow     out.UnitOfWork
	timeout time.Duration
	batch   int
	kinds   []string // enabled kinds, sorted
}

type registeredTaskRunner struct {
	runner out.TaskRunner
	config map[string]any
}

var _ in.RunTasks = (*TaskRunnerLoop)(nil)

// NewTaskRunner registers runners, keeping only the ones enabled in settings
// under events.<kind> (a kind may equal an event handler name and then shares
// that block). It fails on a nil runner, a blank or duplicate kind. A zero
// timeout or batch means the default.
func NewTaskRunner(
	runners []out.TaskRunner,
	settings map[string]EventHandlerSettings,
	deps EventDeps,
	tasks out.TaskRepository,
	outbox out.DestinationOutboxRepository,
	uow out.UnitOfWork,
	timeout time.Duration,
	batch int,
) (*TaskRunnerLoop, error) {
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	if batch <= 0 {
		batch = DefaultTaskBatch
	}
	l := &TaskRunnerLoop{
		runners: make(map[string]registeredTaskRunner, len(runners)),
		deps:    deps, tasks: tasks, outbox: outbox, uow: uow, timeout: timeout, batch: batch,
	}
	seen := make(map[string]bool, len(runners))
	for i, r := range runners {
		if r == nil {
			return nil, fmt.Errorf("task runners: runner %d is nil", i)
		}
		kind := strings.TrimSpace(r.Kind())
		if kind == "" {
			return nil, fmt.Errorf("task runners: runner %d has a blank kind", i)
		}
		if seen[kind] {
			return nil, fmt.Errorf("task runners: duplicate kind %q", kind)
		}
		seen[kind] = true
		s := settings[kind]
		if !s.Enabled {
			deps.Logger.Info("task runner disabled", "kind", kind)
			continue
		}
		l.runners[kind] = registeredTaskRunner{runner: r, config: s.Config}
		l.kinds = append(l.kinds, kind)
		deps.Logger.Info("task runner enabled", "kind", kind)
	}
	sort.Strings(l.kinds)
	return l, nil
}

// Len reports how many runners are enabled.
func (l *TaskRunnerLoop) Len() int {
	if l == nil {
		return 0
	}
	return len(l.runners)
}

// Run implements in.RunTasks. It returns an error only when ctx is done or
// the due tasks cannot be read.
func (l *TaskRunnerLoop) Run(ctx context.Context) (in.RunTasksResult, error) {
	var res in.RunTasksResult
	if l.Len() == 0 {
		return res, nil
	}
	now := l.deps.Clock.Now()
	due, err := l.tasks.Due(ctx, now, l.kinds, l.batch)
	if err != nil {
		return res, fmt.Errorf("task runner: read due tasks: %w", err)
	}
	for _, t := range due {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		rt, ok := l.runners[t.Kind]
		if !ok {
			continue
		}
		l.runTask(ctx, rt, t, &res)
	}
	return res, nil
}

func (l *TaskRunnerLoop) runTask(ctx context.Context, rt registeredTaskRunner, t out.Task, res *in.RunTasksResult) {
	signals, err := l.tasks.Signals(ctx, t.ID)
	if err != nil {
		res.Errors++
		l.deps.Logger.Error("task signals lookup failed", err, "kind", t.Kind, "key", t.Key)
		return
	}
	res.Ran++
	now := l.deps.Clock.Now()
	outcome, kind, err := l.call(ctx, rt, t, signals)
	if ctx.Err() != nil {
		return // shutting down: the task stays as it was
	}
	if kind == outcomeOK {
		err = validateTaskOutcome(outcome, t, signals)
		if err != nil {
			kind = outcomeError
		}
	}
	if kind == outcomeOK {
		enqueued, err := l.persist(ctx, t, outcome, now)
		if err == nil {
			res.Enqueued += enqueued
			return
		}
		l.deps.Logger.Error("task persist failed", err, "kind", t.Kind, "key", t.Key)
		l.fail(ctx, t, now, fmt.Sprintf("task %s could not be saved: %v", t.Key, err))
		res.Errors++
		return
	}
	switch kind {
	case outcomePanic:
		res.Panics++
		l.deps.Logger.Error("task runner panicked", err, "kind", t.Kind, "key", t.Key)
	case outcomeTimeout:
		res.Timeouts++
		l.deps.Logger.Warn("task runner timed out", "kind", t.Kind, "key", t.Key, "timeout", l.timeout.String())
	default:
		res.Errors++
		l.deps.Logger.Error("task runner failed", err, "kind", t.Kind, "key", t.Key)
	}
	l.fail(ctx, t, now, fmt.Sprintf("task %s failed: %v", t.Key, err))
}

// call runs the runner under a deadline and a recover, synchronously.
func (l *TaskRunnerLoop) call(ctx context.Context, rt registeredTaskRunner, t out.Task, signals []out.TaskSignal) (outcome out.TaskOutcome, kind handleOutcome, err error) {
	rctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	defer func() {
		if rec := recover(); rec != nil {
			outcome, kind, err = out.TaskOutcome{}, outcomePanic, fmt.Errorf("%v", rec)
		}
	}()
	tc := out.TaskContext{Clock: l.deps.Clock, Logger: l.deps.Logger, Alerts: l.deps.Alerts, Config: rt.config}
	outcome, err = rt.runner.Run(rctx, tc, t, signals)
	if errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return out.TaskOutcome{}, outcomeTimeout, rctx.Err()
	}
	if err != nil {
		return out.TaskOutcome{}, outcomeError, err
	}
	return outcome, outcomeOK, nil
}

// validateTaskOutcome rejects an outcome the engine could not apply safely.
func validateTaskOutcome(o out.TaskOutcome, t out.Task, signals []out.TaskSignal) error {
	switch o.Status {
	case out.TaskPending, out.TaskWaiting:
		if o.RunAfter.IsZero() {
			return fmt.Errorf("status %s needs a non-zero RunAfter", o.Status)
		}
	case out.TaskDone, out.TaskFailed:
	default:
		return fmt.Errorf("unknown status %q", o.Status)
	}
	if len(o.State) > 0 && !json.Valid(o.State) {
		return errors.New("state is not valid JSON")
	}
	given := make(map[int64]bool, len(signals))
	for _, s := range signals {
		given[s.ID] = true
	}
	for _, id := range o.Consumed {
		if !given[id] {
			return fmt.Errorf("consumed signal %d was not given to the runner", id)
		}
	}
	for _, act := range o.Actions {
		switch a := act.(type) {
		case nil:
		case out.EnqueuePublication:
			if t.ArticleID == "" {
				return errors.New("a publication needs the task to have an article")
			}
			if err := a.Payload.Validate(); err != nil {
				return fmt.Errorf("invalid payload for %s: %w", a.Destination.Normalized(), err)
			}
		default:
			return fmt.Errorf("unknown action %T", act)
		}
	}
	return nil
}

// persist saves the task and enqueues its publications in one transaction.
func (l *TaskRunnerLoop) persist(ctx context.Context, t out.Task, o out.TaskOutcome, now time.Time) (int, error) {
	t.Status = o.Status
	if len(o.State) > 0 {
		t.State = o.State
	}
	if !o.RunAfter.IsZero() {
		t.RunAfter = o.RunAfter
	}
	t.Attempts = 0
	enqueued := 0
	err := l.uow.Do(ctx, func(txCtx context.Context) error {
		enqueued = 0
		if err := l.tasks.Save(txCtx, t, o.Consumed, now); err != nil {
			return fmt.Errorf("save task: %w", err)
		}
		for _, act := range o.Actions {
			a, ok := act.(out.EnqueuePublication)
			if !ok {
				continue
			}
			payload, err := a.Payload.Marshal()
			if err != nil {
				return fmt.Errorf("marshal payload: %w", err)
			}
			if err := l.outbox.EnqueueDestination(txCtx, t.ArticleID, payload, a.Lane, a.Destination, now); err != nil {
				return fmt.Errorf("enqueue: %w", err)
			}
			enqueued++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return enqueued, nil
}

// fail records one more consecutive failure: attempts+1 and an exponential
// delay. State and signals stay as they were. The third failure in a row
// alerts the operator.
func (l *TaskRunnerLoop) fail(ctx context.Context, t out.Task, now time.Time, message string) {
	t.Attempts++
	t.RunAfter = now.Add(taskBackoff(t.Attempts))
	if err := l.tasks.Save(ctx, t, nil, now); err != nil {
		l.deps.Logger.Error("task backoff could not be saved", err, "kind", t.Kind, "key", t.Key)
		return
	}
	if t.Attempts == taskAlertAttempts && l.deps.Alerts != nil {
		l.deps.Alerts.Alert(ctx, t.Kind, message)
	}
}

// taskBackoff is 30s * 2^(attempts-1), capped at TaskMaxBackoff.
func taskBackoff(attempts int) time.Duration {
	d := taskBaseBackoff
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= TaskMaxBackoff {
			return TaskMaxBackoff
		}
	}
	return d
}
