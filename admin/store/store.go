// Package store persists the admin panel's own state (sessions, jobs, job
// logs and the audit trail) in admin.db, separate from the bot's
// operational database.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store/migrations"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

const busyTimeout = 5 * time.Second

// Store is the admin.db handle.
type Store struct {
	db *sql.DB
}

// Job is one admin action execution. Status is one of "queued", "running",
// "succeeded", "failed", "cancelled" or "interrupted".
type Job struct {
	ID, Action, Actor, Status string
	Params                    json.RawMessage
	Result                    *action.Result
	Error                     string
	CreatedAt                 time.Time
	StartedAt, FinishedAt     *time.Time
}

// LogLine is one line of job output; Seq increases per appended line.
type LogLine struct {
	Seq  int64
	At   time.Time
	Line string
}

// AuditEntry records one action invocation by an actor.
type AuditEntry struct {
	ID                                  int64
	At                                  time.Time
	Actor, Action, Kind, Outcome, Error string
	Params                              json.RawMessage
}

// Open opens (creating if needed) admin.db at path and applies its own
// migrations, tracked independently of the operational database.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := infrasqlite.OpenImmediate(path, busyTimeout)
	if err != nil {
		return nil, err
	}
	if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("admin store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// tsLayout is RFC3339 with a fixed nine-digit fraction, so stored text sorts
// chronologically; time.RFC3339Nano parses it.
const tsLayout = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("admin store: parse time %q: %w", v, err)
	}
	return t.UTC(), nil
}

func parseTSPtr(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, err := parseTS(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func paramsText(p json.RawMessage) string {
	if len(p) == 0 {
		return "{}"
	}
	return string(p)
}

func resultText(r *action.Result) (any, error) {
	if r == nil {
		return nil, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("admin store: marshal result: %w", err)
	}
	return string(b), nil
}

// CreateJob inserts a new job.
func (s *Store) CreateJob(ctx context.Context, j Job) error {
	res, err := resultText(j.Result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO admin_jobs (id, action, actor, status, params, result, error, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Action, j.Actor, j.Status, paramsText(j.Params), res, j.Error,
		ts(j.CreatedAt), tsPtr(j.StartedAt), tsPtr(j.FinishedAt))
	if err != nil {
		return fmt.Errorf("admin store: create job: %w", err)
	}
	return nil
}

// UpdateJob overwrites the mutable fields of an existing job (everything
// but ID, Action, Actor and CreatedAt). It errors if the job does not exist.
func (s *Store) UpdateJob(ctx context.Context, j Job) error {
	res, err := resultText(j.Result)
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, `
		UPDATE admin_jobs SET status = ?, params = ?, result = ?, error = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		j.Status, paramsText(j.Params), res, j.Error, tsPtr(j.StartedAt), tsPtr(j.FinishedAt), j.ID)
	if err != nil {
		return fmt.Errorf("admin store: update job: %w", err)
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("admin store: update job %q: not found", j.ID)
	}
	return nil
}

const jobCols = `id, action, actor, status, params, result, error, created_at, started_at, finished_at`

type scanner interface{ Scan(...any) error }

func scanJob(r scanner) (Job, error) {
	var (
		j                 Job
		params            string
		result            sql.NullString
		created           string
		started, finished sql.NullString
	)
	if err := r.Scan(&j.ID, &j.Action, &j.Actor, &j.Status, &params, &result, &j.Error, &created, &started, &finished); err != nil {
		return Job{}, err
	}
	j.Params = json.RawMessage(params)
	if result.Valid {
		var res action.Result
		if err := json.Unmarshal([]byte(result.String), &res); err != nil {
			return Job{}, fmt.Errorf("admin store: decode result: %w", err)
		}
		j.Result = &res
	}
	var err error
	if j.CreatedAt, err = parseTS(created); err != nil {
		return Job{}, err
	}
	if j.StartedAt, err = parseTSPtr(started); err != nil {
		return Job{}, err
	}
	if j.FinishedAt, err = parseTSPtr(finished); err != nil {
		return Job{}, err
	}
	return j, nil
}

// GetJob returns the job with the given id; ok is false when absent.
func (s *Store) GetJob(ctx context.Context, id string) (Job, bool, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM admin_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("admin store: get job: %w", err)
	}
	return j, true, nil
}

// ListJobs returns up to limit jobs, newest first.
func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM admin_jobs ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("admin store: list jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("admin store: list jobs: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// AppendLog adds a log line to a job and returns its sequence number.
func (s *Store) AppendLog(ctx context.Context, jobID, line string, at time.Time) (int64, error) {
	r, err := s.db.ExecContext(ctx, `INSERT INTO admin_job_logs (job_id, at, line) VALUES (?, ?, ?)`, jobID, ts(at), line)
	if err != nil {
		return 0, fmt.Errorf("admin store: append log: %w", err)
	}
	return r.LastInsertId()
}

// Logs returns the job's log lines with Seq > afterSeq, in ascending order.
func (s *Store) Logs(ctx context.Context, jobID string, afterSeq int64) ([]LogLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, at, line FROM admin_job_logs WHERE job_id = ? AND seq > ? ORDER BY seq`, jobID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("admin store: logs: %w", err)
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var (
			l  LogLine
			at string
		)
		if err := rows.Scan(&l.Seq, &at, &l.Line); err != nil {
			return nil, fmt.Errorf("admin store: logs: %w", err)
		}
		if l.At, err = parseTS(at); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// MarkRunningInterrupted marks every queued or running job as interrupted,
// finished at now, and returns how many were changed. Called at startup.
func (s *Store) MarkRunningInterrupted(ctx context.Context, now time.Time) (int, error) {
	r, err := s.db.ExecContext(ctx, `
		UPDATE admin_jobs SET status = 'interrupted', finished_at = ? WHERE status IN ('queued', 'running')`, ts(now))
	if err != nil {
		return 0, fmt.Errorf("admin store: mark interrupted: %w", err)
	}
	n, err := r.RowsAffected()
	return int(n), err
}

// AddAudit appends an audit entry; the ID field is ignored.
func (s *Store) AddAudit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO admin_audit (at, actor, action, kind, outcome, error, params) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts(e.At), e.Actor, e.Action, e.Kind, e.Outcome, e.Error, paramsText(e.Params))
	if err != nil {
		return fmt.Errorf("admin store: add audit: %w", err)
	}
	return nil
}

// ListAudit returns up to limit audit entries, newest first.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, at, actor, action, kind, outcome, error, params FROM admin_audit ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("admin store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var (
			e          AuditEntry
			at, params string
		)
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Kind, &e.Outcome, &e.Error, &params); err != nil {
			return nil, fmt.Errorf("admin store: list audit: %w", err)
		}
		if e.At, err = parseTS(at); err != nil {
			return nil, err
		}
		e.Params = json.RawMessage(params)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CreateSession stores a session keyed by the hash of its token.
func (s *Store) CreateSession(ctx context.Context, tokenHash, username string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_sessions (token_hash, username, expires_at) VALUES (?, ?, ?)`,
		tokenHash, username, ts(expires))
	if err != nil {
		return fmt.Errorf("admin store: create session: %w", err)
	}
	return nil
}

// TouchSession validates a session and slides its expiry to newExpiry.
// ok is false when the session is missing or already expired (expires_at <= now).
func (s *Store) TouchSession(ctx context.Context, tokenHash string, now, newExpiry time.Time) (string, bool, error) {
	var username string
	err := s.db.QueryRowContext(ctx, `
		UPDATE admin_sessions SET expires_at = ? WHERE token_hash = ? AND expires_at > ? RETURNING username`,
		ts(newExpiry), tokenHash, ts(now)).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("admin store: touch session: %w", err)
	}
	return username, true, nil
}

// DeleteSession removes a session (logout); a missing session is not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("admin store: delete session: %w", err)
	}
	return nil
}

// PurgeExpiredSessions deletes sessions whose expiry is at or before now.
func (s *Store) PurgeExpiredSessions(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at <= ?`, ts(now)); err != nil {
		return fmt.Errorf("admin store: purge sessions: %w", err)
	}
	return nil
}
