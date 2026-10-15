-- migrations/0024_editorial_cycle_session_lifecycle.sql
--
-- Editorial cycle session state machine.
--
-- editorial_cycle_sessions.status has only ever allowed
-- ('running','completed','abandoned') since migration 0012, and nothing
-- in this codebase has ever written 'abandoned' — it is dead. This
-- migration expands the CHECK constraint to
-- ('running','paused','completed','failed','cancelled'), giving the
-- session lifecycle a real "interrupted, resumable" state ('paused') and
-- a real "interrupted, not resumable" state ('cancelled'), plus 'failed'
-- for a session that ended in an unrecoverable error rather than either
-- of those.
--
-- SQLite has no ALTER TABLE support for changing a CHECK constraint, so
-- this rebuilds the table: create a replacement with the corrected
-- constraint, copy every row across (remapping status), drop the
-- original, rename the replacement into place, then recreate its index.
-- All other columns, defaults, and foreign keys are preserved exactly as
-- migration 0012 left them.
--
-- Status remap for existing rows: no process can still legitimately own
-- a 'running' row that survived to see a fresh migration run — the
-- process that set it either finished (and would have written
-- 'completed') or died, so 'running' rows are remapped to 'paused'
-- (interrupted, resumable) rather than kept as a lie about a still-active
-- session. 'abandoned' is always dead — nothing ever wrote it — and maps
-- to 'cancelled', its closest equivalent in the new vocabulary. See the
-- design spec's §11.1.
--
-- Foreign key safety: editorial_cycle_sessions is the parent of
-- editorial_review_decisions (ON DELETE CASCADE, migration 0012). With
-- foreign key enforcement on, DROP TABLE performs an implicit DELETE of
-- every row first, which would fire that cascade and silently wipe every
-- review decision. This migration relies on
-- infrastructure/sqlite.Migrate's applyMigration disabling foreign key
-- enforcement around the whole operation (and running
-- PRAGMA foreign_key_check immediately after) for exactly this reason —
-- see that function's doc comment, and migrations 0017/0023 for the same
-- established pattern. Do not run this file's SQL directly against a
-- connection with foreign_keys=ON and existing data. Row ids are
-- preserved by the INSERT...SELECT below (explicit id column, no
-- renumbering), so editorial_review_decisions.session_id references
-- remain valid after the rename.
--
-- editorial_cycle_session_candidates is new: it will hold, per session,
-- the frozen set of article candidates a review session was shown at
-- the time it started (score/selected_at), so a session resumed later
-- reviews the same candidate set it began with rather than a
-- recomputed one. Both its foreign keys cascade on delete, so it never
-- outlives the session or article it belongs to.
--
-- Rollback: rebuild editorial_cycle_sessions again with the pre-0024
-- CHECK constraint (migration 0012's list: 'running','completed',
-- 'abandoned'), after first confirming no row has status IN
-- ('paused','failed','cancelled') — any that do must be resolved or
-- manually remapped to the nearest pre-0024 equivalent ('paused'/
-- 'failed'/'cancelled' -> 'abandoned') before the rollback migration
-- runs, or they will violate the restored constraint. Also drop
-- editorial_cycle_session_candidates.

CREATE TABLE editorial_cycle_sessions_new (
    id            TEXT PRIMARY KEY,
    started_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','paused','completed','failed','cancelled')),
    current_step  TEXT NOT NULL DEFAULT 'review',
    flags_json    TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO editorial_cycle_sessions_new (id, started_at, updated_at, status, current_step, flags_json)
SELECT id, started_at, updated_at,
    CASE status
        WHEN 'running' THEN 'paused'
        WHEN 'abandoned' THEN 'cancelled'
        ELSE status
    END,
    current_step, flags_json
FROM editorial_cycle_sessions;

DROP TABLE editorial_cycle_sessions;
ALTER TABLE editorial_cycle_sessions_new RENAME TO editorial_cycle_sessions;
CREATE INDEX idx_editorial_cycle_sessions_status ON editorial_cycle_sessions (status);

CREATE TABLE editorial_cycle_session_candidates (
    session_id  TEXT NOT NULL REFERENCES editorial_cycle_sessions(id) ON DELETE CASCADE,
    article_id  TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    score       REAL NOT NULL,
    selected_at TIMESTAMP NOT NULL,
    PRIMARY KEY (session_id, article_id)
);
CREATE INDEX idx_session_candidates_session ON editorial_cycle_session_candidates (session_id);
