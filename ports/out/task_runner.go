package out

import (
	"context"
	"encoding/json"
	"time"
)

// TaskRunner is a host-supplied plugin that advances background tasks of one
// Kind (see Task). A Kind may equal an EventHandler name; they then share the
// settings block events.<name>. Kinds are unique among runners.
type TaskRunner interface {
	// Kind is the task kind this runner owns.
	Kind() string
	// Run advances the task. It runs outside any transaction and must honour
	// ctx (it carries a deadline).
	Run(ctx context.Context, tc TaskContext, t Task, signals []TaskSignal) (TaskOutcome, error)
}

// TaskContext is what the engine lends a runner: services only.
type TaskContext struct {
	Clock  Clock
	Logger Logger
	Alerts EventAlertSink
	// Config is events.<kind>.config.
	Config map[string]any
}

// TaskOutcome is the result of a successful TaskRunner.Run.
type TaskOutcome struct {
	// Status is the new status; pending and waiting require a non-zero RunAfter.
	Status TaskStatus
	// State is the new opaque state; nil leaves it unchanged.
	State    json.RawMessage
	RunAfter time.Time
	// Consumed lists the signals Run processed; the rest stay pending.
	Consumed []int64
	// Actions may only be EnqueuePublication; they are enqueued in the same
	// transaction as the task update, on a placeholder article of the task's
	// own (not Task.ArticleID), so they have no freshness expiry and do not
	// collide with the originating article's rows. One row per destination
	// per task: a publication that inserts no row fails the outcome.
	Actions []EventAction
}
