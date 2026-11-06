CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    username   TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE TABLE admin_jobs (
    id          TEXT PRIMARY KEY,
    action      TEXT NOT NULL,
    actor       TEXT NOT NULL,
    status      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}',
    result      TEXT,
    error       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT
);
CREATE INDEX idx_admin_jobs_created ON admin_jobs (created_at DESC);

CREATE TABLE admin_job_logs (
    seq    INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL REFERENCES admin_jobs (id) ON DELETE CASCADE,
    at     TEXT NOT NULL,
    line   TEXT NOT NULL
);
CREATE INDEX idx_admin_job_logs_job ON admin_job_logs (job_id, seq);

CREATE TABLE admin_audit (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      TEXT NOT NULL,
    actor   TEXT NOT NULL,
    action  TEXT NOT NULL,
    kind    TEXT NOT NULL,
    outcome TEXT NOT NULL,
    error   TEXT NOT NULL DEFAULT '',
    params  TEXT NOT NULL DEFAULT '{}'
);
