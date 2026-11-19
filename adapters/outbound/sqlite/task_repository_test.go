package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var taskNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func TestTaskEnqueueIsIdempotentPerKindKey(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewTaskRepository(db)
	ctx := context.Background()

	for i, want := range []bool{true, false} {
		created, err := repo.Enqueue(ctx, "k", "key", "a1", nil, taskNow)
		if err != nil || created != want {
			t.Fatalf("Enqueue #%d = %v, %v; want %v, nil", i+1, created, err, want)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM tasks`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("tasks rows = %d, %v; want 1", rows, err)
	}
}

func TestTaskDueIncludesPendingAndSignalledWaiting(t *testing.T) {
	repo := sqliteadapter.NewTaskRepository(newTestDB(t))
	ctx := context.Background()

	enqueue := func(key string, at time.Time) out.Task {
		t.Helper()
		if _, err := repo.Enqueue(ctx, "k", key, "", nil, at); err != nil {
			t.Fatal(err)
		}
		due, err := repo.Due(ctx, at.Add(time.Hour*24*365), []string{"k"}, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range due {
			if d.Key == key {
				return d
			}
		}
		t.Fatalf("task %s not found", key)
		return out.Task{}
	}
	set := func(task out.Task, status out.TaskStatus, runAfter time.Time) {
		t.Helper()
		task.Status, task.RunAfter = status, runAfter
		if err := repo.Save(ctx, task, nil, taskNow); err != nil {
			t.Fatal(err)
		}
	}

	// Fractional seconds exercise the time comparison, which is not lexical.
	frac := taskNow.Add(500 * time.Millisecond)
	set(enqueue("pending-due", taskNow), out.TaskPending, taskNow)
	set(enqueue("pending-future", taskNow), out.TaskPending, frac)
	set(enqueue("waiting-signal", taskNow), out.TaskWaiting, taskNow.Add(time.Hour))
	set(enqueue("waiting-idle", taskNow), out.TaskWaiting, taskNow.Add(time.Hour))
	set(enqueue("waiting-deadline", taskNow), out.TaskWaiting, taskNow.Add(-time.Minute))
	set(enqueue("done", taskNow), out.TaskDone, taskNow.Add(-time.Hour))

	byKey := map[string]out.Task{}
	all, _ := repo.Due(ctx, taskNow.Add(24*time.Hour*365), []string{"k"}, 100)
	for _, d := range all {
		byKey[d.Key] = d
	}
	if err := repo.AddSignal(ctx, byKey["waiting-signal"].ID, nil, taskNow); err != nil {
		t.Fatal(err)
	}

	due, err := repo.Due(ctx, taskNow, []string{"k"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range due {
		got = append(got, d.Key)
	}
	want := []string{"waiting-deadline", "pending-due", "waiting-signal"}
	if len(got) != len(want) {
		t.Fatalf("Due = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Due = %v, want %v", got, want)
		}
	}

	if due, _ := repo.Due(ctx, taskNow, []string{"k"}, 1); len(due) != 1 {
		t.Fatalf("Due limit 1 returned %d tasks", len(due))
	}
	// Just past the fractional run_after the future task becomes due.
	due, _ = repo.Due(ctx, frac, []string{"k"}, 100)
	found := false
	for _, d := range due {
		found = found || d.Key == "pending-future"
	}
	if !found {
		t.Fatalf("pending-future not due at its run_after: %+v", due)
	}
}

func TestTaskSaveConsumesSignals(t *testing.T) {
	repo := sqliteadapter.NewTaskRepository(newTestDB(t))
	ctx := context.Background()
	if _, err := repo.Enqueue(ctx, "k", "key", "a1", nil, taskNow); err != nil {
		t.Fatal(err)
	}
	task, found, err := repo.Get(ctx, 1)
	if err != nil || !found {
		t.Fatalf("Get = %v, %v", found, err)
	}
	for _, p := range []string{`{"n":1}`, `{"n":2}`} {
		if err := repo.AddSignal(ctx, task.ID, []byte(p), taskNow); err != nil {
			t.Fatal(err)
		}
	}
	signals, err := repo.Signals(ctx, task.ID)
	if err != nil || len(signals) != 2 || string(signals[0].Payload) != `{"n":1}` || !signals[0].CreatedAt.Equal(taskNow) {
		t.Fatalf("Signals = %+v, %v", signals, err)
	}

	later := taskNow.Add(time.Minute)
	task.Status, task.Attempts, task.RunAfter = out.TaskWaiting, 2, later.Add(time.Hour)
	task.State = []byte(`{"step":2}`)
	if err := repo.Save(ctx, task, []int64{signals[0].ID}, later); err != nil {
		t.Fatal(err)
	}
	left, _ := repo.Signals(ctx, task.ID)
	if len(left) != 1 || left[0].ID != signals[1].ID {
		t.Fatalf("Signals after Save = %+v, want only the second", left)
	}
	got, _, _ := repo.Get(ctx, task.ID)
	if got.Status != out.TaskWaiting || got.Attempts != 2 || string(got.State) != `{"step":2}` ||
		!got.RunAfter.Equal(later.Add(time.Hour)) || !got.UpdatedAt.Equal(later) || !got.CreatedAt.Equal(taskNow) {
		t.Fatalf("saved task = %+v", got)
	}

	if err := repo.Save(ctx, out.Task{ID: 999, Status: out.TaskDone}, nil, later); !errors.Is(err, out.ErrTaskNotFound) {
		t.Fatalf("Save(missing) = %v, want ErrTaskNotFound", err)
	}
}

func TestAddSignalToClosedTask(t *testing.T) {
	repo := sqliteadapter.NewTaskRepository(newTestDB(t))
	ctx := context.Background()
	if _, err := repo.Enqueue(ctx, "k", "key", "", nil, taskNow); err != nil {
		t.Fatal(err)
	}
	task, _, _ := repo.Get(ctx, 1)
	task.Status = out.TaskDone
	if err := repo.Save(ctx, task, nil, taskNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSignal(ctx, task.ID, nil, taskNow); !errors.Is(err, out.ErrTaskClosed) {
		t.Fatalf("AddSignal(done) = %v, want ErrTaskClosed", err)
	}
	if err := repo.AddSignal(ctx, 999, nil, taskNow); !errors.Is(err, out.ErrTaskNotFound) {
		t.Fatalf("AddSignal(missing) = %v, want ErrTaskNotFound", err)
	}
}

func TestTaskEmptyStateReadsBackAsObject(t *testing.T) {
	repo := sqliteadapter.NewTaskRepository(newTestDB(t))
	ctx := context.Background()
	if _, err := repo.Enqueue(ctx, "k", "key", "", nil, taskNow); err != nil {
		t.Fatal(err)
	}
	got, _, _ := repo.Get(ctx, 1)
	if string(got.State) != `{}` {
		t.Fatalf("State = %q, want {}", got.State)
	}
}

func TestTaskOperationsJoinUnitOfWork(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewTaskRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	ctx := context.Background()
	boom := errors.New("boom")

	err := uow.Do(ctx, func(ctx context.Context) error {
		if created, err := repo.Enqueue(ctx, "k", "key", "", nil, taskNow); err != nil || !created {
			t.Fatalf("Enqueue in tx = %v, %v", created, err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do error = %v, want boom", err)
	}
	if _, found, _ := repo.Get(ctx, 1); found {
		t.Fatal("task survived a rolled-back unit of work")
	}
}

func TestTaskDueFiltersKindsAndFailedTasksIgnoreSignals(t *testing.T) {
	repo := sqliteadapter.NewTaskRepository(newTestDB(t))
	ctx := context.Background()
	for _, kk := range [][2]string{{"a", "1"}, {"b", "1"}} {
		if _, err := repo.Enqueue(ctx, kk[0], kk[1], "", nil, taskNow); err != nil {
			t.Fatal(err)
		}
	}
	if due, _ := repo.Due(ctx, taskNow, nil, 10); len(due) != 0 {
		t.Fatalf("no kinds returned %d tasks", len(due))
	}
	due, _ := repo.Due(ctx, taskNow, []string{"b"}, 10)
	if len(due) != 1 || due[0].Kind != "b" {
		t.Fatalf("Due(b) = %+v", due)
	}

	// A failed task (attempts > 0) with a signal waits for its run_after.
	task := due[0]
	task.Attempts, task.RunAfter = 1, taskNow.Add(time.Minute)
	if err := repo.Save(ctx, task, nil, taskNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSignal(ctx, task.ID, nil, taskNow); err != nil {
		t.Fatal(err)
	}
	if due, _ := repo.Due(ctx, taskNow, []string{"b"}, 10); len(due) != 0 {
		t.Fatalf("failed task with a signal is due before its backoff: %+v", due)
	}
	if due, _ := repo.Due(ctx, taskNow.Add(time.Minute), []string{"b"}, 10); len(due) != 1 {
		t.Fatalf("failed task not due at its run_after: %+v", due)
	}
}
