package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// TaskRepository implements ports/out.TaskRepository against SQLite.
type TaskRepository struct {
	db *sql.DB
}

// NewTaskRepository builds a TaskRepository over an already-open,
// already-migrated database.
func NewTaskRepository(db *sql.DB) *TaskRepository {
	return &TaskRepository{db: db}
}

var _ appout.TaskRepository = (*TaskRepository)(nil)

func normalizeJSON(raw json.RawMessage, what string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("sqlite: %s is not valid JSON", what)
	}
	return raw, nil
}

// Enqueue implements ports/out.TaskRepository.
func (r *TaskRepository) Enqueue(ctx context.Context, kind, key, articleID string, state json.RawMessage, now time.Time) (bool, error) {
	if kind == "" || key == "" {
		return false, errors.New("sqlite: enqueue task: kind and key are required")
	}
	state, err := normalizeJSON(state, "task state")
	if err != nil {
		return false, fmt.Errorf("sqlite: enqueue task %s/%s: %w", kind, key, err)
	}
	res, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO tasks (kind, task_key, article_id, status, state, attempts, run_after, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, 0, ?, ?, ?)
		ON CONFLICT(kind, task_key) DO NOTHING
	`, kind, key, articleID, string(state), sqliteTime(now), sqliteTime(now), sqliteTime(now))
	if err != nil {
		return false, fmt.Errorf("sqlite: enqueue task %s/%s: %w", kind, key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	return n == 1, nil
}

const taskColumns = `id, kind, task_key, article_id, status, state, attempts, run_after, created_at, updated_at`

func scanTask(s rowScanner) (appout.Task, error) {
	var (
		t         appout.Task
		status    string
		state     string
		runAfter  sqliteNullTime
		createdAt sqliteNullTime
		updatedAt sqliteNullTime
	)
	if err := s.Scan(&t.ID, &t.Kind, &t.Key, &t.ArticleID, &status, &state, &t.Attempts, &runAfter, &createdAt, &updatedAt); err != nil {
		return appout.Task{}, err
	}
	t.Status = appout.TaskStatus(status)
	t.State = json.RawMessage(state)
	t.RunAfter, t.CreatedAt, t.UpdatedAt = runAfter.Time, createdAt.Time, updatedAt.Time
	return t, nil
}

// Due implements ports/out.TaskRepository (kinds filtered, signals wake only
// tasks that have not failed). Timestamps are RFC3339Nano text
// whose fraction length varies, so they are compared and ordered through
// julianday() rather than lexically.
func (r *TaskRepository) Due(ctx context.Context, now time.Time, kinds []string, limit int) ([]appout.Task, error) {
	if limit <= 0 || len(kinds) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(kinds)+2)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, sqliteTime(now), limit)
	marks := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT `+taskColumns+` FROM tasks
		WHERE status IN ('pending','waiting')
		  AND kind IN (`+marks+`)
		  AND (julianday(run_after) <= julianday(?)
		       OR (attempts = 0
		           AND EXISTS (SELECT 1 FROM task_signals s WHERE s.task_id = tasks.id AND s.consumed_at IS NULL)))
		ORDER BY julianday(run_after), id
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list due tasks: %w", err)
	}
	defer rows.Close()
	var tasks []appout.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list due tasks: %w", err)
	}
	return tasks, nil
}

// Get implements ports/out.TaskRepository.
func (r *TaskRepository) Get(ctx context.Context, id int64) (appout.Task, bool, error) {
	t, err := scanTask(executor(ctx, r.db).QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return appout.Task{}, false, nil
	}
	if err != nil {
		return appout.Task{}, false, fmt.Errorf("sqlite: get task %d: %w", id, err)
	}
	return t, true, nil
}

// Signals implements ports/out.TaskRepository.
func (r *TaskRepository) Signals(ctx context.Context, taskID int64) ([]appout.TaskSignal, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT id, task_id, payload, created_at FROM task_signals
		WHERE task_id = ? AND consumed_at IS NULL ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list signals of task %d: %w", taskID, err)
	}
	defer rows.Close()
	var signals []appout.TaskSignal
	for rows.Next() {
		var (
			s         appout.TaskSignal
			payload   string
			createdAt sqliteNullTime
		)
		if err := rows.Scan(&s.ID, &s.TaskID, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan task signal: %w", err)
		}
		s.Payload = json.RawMessage(payload)
		s.CreatedAt = createdAt.Time
		signals = append(signals, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list signals of task %d: %w", taskID, err)
	}
	return signals, nil
}

// AddSignal implements ports/out.TaskRepository. The status check is part of
// the INSERT, so a task closed concurrently never gains a signal.
func (r *TaskRepository) AddSignal(ctx context.Context, taskID int64, payload json.RawMessage, now time.Time) error {
	payload, err := normalizeJSON(payload, "signal payload")
	if err != nil {
		return fmt.Errorf("sqlite: add signal to task %d: %w", taskID, err)
	}
	ex := executor(ctx, r.db)
	res, err := ex.ExecContext(ctx, `
		INSERT INTO task_signals (task_id, payload, created_at)
		SELECT id, ?, ? FROM tasks WHERE id = ? AND status IN ('pending','waiting')`,
		string(payload), sqliteTime(now), taskID)
	if err != nil {
		return fmt.Errorf("sqlite: add signal to task %d: %w", taskID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 1 {
		return nil
	}
	var status string
	err = ex.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite: add signal to task %d: %w", taskID, appout.ErrTaskNotFound)
	}
	if err != nil {
		return fmt.Errorf("sqlite: add signal to task %d: %w", taskID, err)
	}
	return fmt.Errorf("sqlite: add signal to task %d (%s): %w", taskID, status, appout.ErrTaskClosed)
}

// Save implements ports/out.TaskRepository.
func (r *TaskRepository) Save(ctx context.Context, t appout.Task, consumed []int64, now time.Time) error {
	state, err := normalizeJSON(t.State, "task state")
	if err != nil {
		return fmt.Errorf("sqlite: save task %d: %w", t.ID, err)
	}
	switch t.Status {
	case appout.TaskPending, appout.TaskWaiting, appout.TaskDone, appout.TaskFailed:
	default:
		return fmt.Errorf("sqlite: save task %d: unknown status %q", t.ID, t.Status)
	}
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET status = ?, state = ?, attempts = ?, run_after = ?, updated_at = ?
			WHERE id = ?`,
			string(t.Status), string(state), t.Attempts, sqliteTime(t.RunAfter), sqliteTime(now), t.ID)
		if err != nil {
			return fmt.Errorf("sqlite: save task %d: %w", t.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("sqlite: rows affected: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("sqlite: save task %d: %w", t.ID, appout.ErrTaskNotFound)
		}
		if len(consumed) == 0 {
			return nil
		}
		args := []any{sqliteTime(now), t.ID}
		for _, id := range consumed {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_signals SET consumed_at = ?
			WHERE task_id = ? AND consumed_at IS NULL AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(consumed)), ",")+`)`,
			args...); err != nil {
			return fmt.Errorf("sqlite: consume signals of task %d: %w", t.ID, err)
		}
		return nil
	})
}
