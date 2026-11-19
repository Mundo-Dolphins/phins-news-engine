package out

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// TaskStatus is the lifecycle state of a background task.
type TaskStatus string

const (
	TaskPending TaskStatus = "pending" // work to do as soon as RunAfter <= now
	TaskWaiting TaskStatus = "waiting" // waiting for a signal or until RunAfter (deadline)
	TaskDone    TaskStatus = "done"
	TaskFailed  TaskStatus = "failed"
)

// Task is a unit of background work identified by (Kind, Key).
type Task struct {
	ID        int64
	Kind      string
	Key       string
	ArticleID string
	Status    TaskStatus
	State     json.RawMessage // opaque to the engine; never empty ({} by default)
	Attempts  int             // consecutive Run failures; reset to 0 after a successful Run
	RunAfter  time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskSignal is an external input addressed to a task.
type TaskSignal struct {
	ID        int64
	TaskID    int64
	Payload   json.RawMessage
	CreatedAt time.Time
}

// TaskRepository stores tasks and their signals. Every operation joins the
// unit of work carried by ctx, if any.
type TaskRepository interface {
	// Enqueue creates the task (pending, RunAfter=now) if (kind,key) does not
	// exist; created is false when it already existed.
	Enqueue(ctx context.Context, kind, key, articleID string, state json.RawMessage, now time.Time) (created bool, err error)
	// Due returns tasks of the given kinds (empty kinds: none), pending|waiting,
	// with run_after <= now, or with unconsumed signals when Attempts == 0
	// (a failed task waits for its backoff even if signals are queued).
	// Ordered by run_after, up to limit. Runners must list every signal they
	// handled in TaskOutcome.Consumed, or the task is woken again by it.
	Due(ctx context.Context, now time.Time, kinds []string, limit int) ([]Task, error)
	Get(ctx context.Context, id int64) (Task, bool, error)
	// Signals returns the task's unconsumed signals, by id.
	Signals(ctx context.Context, taskID int64) ([]TaskSignal, error)
	// AddSignal fails with ErrTaskNotFound if the task does not exist and
	// ErrTaskClosed if it is done|failed.
	AddSignal(ctx context.Context, taskID int64, payload json.RawMessage, now time.Time) error
	// Save writes Status, State, Attempts and RunAfter and marks the given
	// signals consumed.
	Save(ctx context.Context, t Task, consumed []int64, now time.Time) error
}

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskClosed   = errors.New("task is closed")
)
