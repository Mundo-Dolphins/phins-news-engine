-- migrations/0050_tasks.sql
--
-- Background tasks and their signals: a generic store for multi-step work
-- that spans time (wait for a signal or a deadline, then act). A task is
-- identified by (kind, task_key), so enqueueing the same work twice is a
-- no-op. state is opaque JSON owned by the host's runner for that kind.
--
-- Only the task runner writes tasks rows; producers of external input
-- (pollers) only insert task_signals rows, so the two never contend on the
-- same table. A runner consumes signals by setting consumed_at.
--
-- Production writes these tables, so they are normal merged tables, not
-- part of sqlite.localOwnedTables.
--
-- Rollback (operational -- this codebase has no down-migrations): purely
-- additive. To revert: `DROP TABLE task_signals; DROP TABLE tasks;` and
-- delete this migration's row from schema_migrations to let it reapply.

CREATE TABLE tasks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL,
    task_key   TEXT NOT NULL,
    article_id TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','waiting','done','failed')),
    state      TEXT NOT NULL DEFAULT '{}',
    attempts   INTEGER NOT NULL DEFAULT 0,
    run_after  TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (kind, task_key)
);
CREATE INDEX idx_tasks_status_run_after ON tasks (status, run_after);

CREATE TABLE task_signals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    payload     TEXT NOT NULL DEFAULT '{}',
    created_at  TIMESTAMP NOT NULL,
    consumed_at TIMESTAMP
);
CREATE INDEX idx_task_signals_unconsumed ON task_signals (task_id) WHERE consumed_at IS NULL;
