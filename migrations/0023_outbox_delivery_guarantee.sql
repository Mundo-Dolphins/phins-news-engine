-- migrations/0023_outbox_delivery_guarantee.sql
--
-- Closes the crash window described in ADR 0007/0016's "known risk": if
-- the process dies after Telegram accepts sendMessage (or after a
-- transport timeout whose outcome is genuinely unknown) but before the
-- outbox item's finalize transaction commits, the item was left
-- 'claimed' with zero local evidence of the attempt — the next
-- stale-claim reclaim put it straight back in the claimable pool and
-- PublishOutbox resent it blindly. See ADR 0030.
--
-- publication_outbox gains a new status, 'sent_unconfirmed', reachable
-- only from 'leased' (renamed from 'claimed') via the new
-- RecordSentUnconfirmed write PublishOutbox now makes immediately after
-- Publisher.Publish returns, before any other bookkeeping — see
-- usecase.PublishOutbox.recordSentThenFinalize/recordAmbiguous. Crash
-- recovery's stale-claim reclaim (ClaimBatch) only ever touches rows
-- still in 'leased': a row that reached 'sent_unconfirmed' is never
-- silently reclaimed back into the claimable pool, which is the
-- invariant that makes blind resend impossible from this point on. Two
-- more states close the loop: 'retryable' (a confirmed temporary failure,
-- or a negatively-reconciled ambiguous item, waiting out its backoff —
-- previously indistinguishable from a never-attempted 'pending' row) and
-- 'quarantined' (Reconcile could not determine the outcome
-- deterministically — held for manual repair via `phins-news-engine-tools
-- telegram outbox repair`, never auto-retried).
--
-- sent_message_id/sent_at/ambiguous_reason/quarantined_at persist
-- whatever evidence a publish attempt produced, in transactional order
-- (see the design doc's "Transactional ordering" section): this is the
-- durable record a restarted process reads to reconcile deterministically
-- instead of guessing.
--
-- publication_attempts.outcome gains 'ambiguous' for the same reason.
--
-- SQLite has no ALTER TABLE support for changing a CHECK constraint, so
-- this rebuilds both tables: create a replacement with the corrected
-- constraint and new columns, copy every row across (remapping status on
-- publication_outbox), drop the original, rename the replacement into
-- place, then recreate its indexes. All existing columns, defaults, and
-- foreign keys are preserved exactly as migrations 0004/0011 left them;
-- publication_attempts gained no new columns, only the CHECK constraint's
-- value list changed.
--
-- Foreign key safety: publication_outbox is the parent of
-- publication_attempts (ON DELETE CASCADE) and is referenced by
-- event_cluster_publications.outbox_id (ON DELETE SET NULL). With foreign
-- key enforcement on, DROP TABLE performs an implicit DELETE of every row
-- first, which would fire those cascades/SET NULLs and silently wipe or
-- detach related rows. This migration relies on
-- infrastructure/sqlite.Migrate's applyMigration disabling foreign key
-- enforcement around the whole operation (and running
-- PRAGMA foreign_key_check immediately after) for exactly this reason —
-- see that function's doc comment. Do not run this file's SQL directly
-- against a connection with foreign_keys=ON and existing data. Row ids
-- are preserved by the INSERT...SELECT below (explicit id column, no
-- AUTOINCREMENT renumbering), so event_cluster_publications.outbox_id
-- references remain valid after the rename.
--
-- Status remap for existing rows: 'pending'->'pending' (unchanged),
-- 'claimed'->'leased' (rename only, same meaning), 'published'->
-- 'delivered' (rename only, same meaning), 'dead'->'dead' (unchanged).
-- No existing row can be 'sent_unconfirmed'/'retryable'/'quarantined'
-- (those states didn't exist before this migration), so the mapping is
-- total and lossless.
--
-- Rollback: rebuild both tables again with their pre-0023 CHECK
-- constraints and column sets (see migrations 0004/0011 for the
-- pre-0023 shape), after first confirming no row has status IN
-- ('sent_unconfirmed','retryable','quarantined') or outcome='ambiguous'
-- — any that do must be resolved (via `telegram outbox repair`) or
-- manually remapped to the nearest pre-0023 equivalent
-- ('sent_unconfirmed'/'retryable'->'pending' or 'claimed' depending on
-- whether a lease is still held, 'quarantined'->'dead') before the
-- rollback migration runs, or they will violate the restored constraint.

CREATE TABLE publication_outbox_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id        TEXT NOT NULL UNIQUE REFERENCES articles(id) ON DELETE CASCADE,
    payload           TEXT NOT NULL DEFAULT '{}',
    status            TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending','leased','sent_unconfirmed','delivered','retryable','quarantined','dead'
    )),
    available_at      TIMESTAMP NOT NULL,
    claimed_at        TIMESTAMP,
    claimed_by        TEXT NOT NULL DEFAULT '',
    attempt_count     INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT NOT NULL DEFAULT '',
    sent_message_id   TEXT NOT NULL DEFAULT '',
    sent_at           TIMESTAMP,
    ambiguous_reason  TEXT NOT NULL DEFAULT '',
    quarantined_at    TIMESTAMP,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO publication_outbox_new (
    id, article_id, payload, status, available_at, claimed_at, claimed_by,
    attempt_count, last_error, created_at, updated_at
)
SELECT
    id, article_id, payload,
    CASE status
        WHEN 'claimed' THEN 'leased'
        WHEN 'published' THEN 'delivered'
        ELSE status
    END,
    available_at, claimed_at, claimed_by, attempt_count, last_error, created_at, updated_at
FROM publication_outbox;

DROP TABLE publication_outbox;
ALTER TABLE publication_outbox_new RENAME TO publication_outbox;

CREATE INDEX idx_outbox_status_available ON publication_outbox (status, available_at);
CREATE INDEX idx_outbox_status_sent_at ON publication_outbox (status, sent_at);

CREATE TABLE publication_attempts_new (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    outbox_id            INTEGER NOT NULL REFERENCES publication_outbox(id) ON DELETE CASCADE,
    article_id           TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    attempted_at         TIMESTAMP NOT NULL,
    outcome              TEXT NOT NULL CHECK (outcome IN ('succeeded','ambiguous','temporary_failure','permanent_failure')),
    error_message        TEXT NOT NULL DEFAULT '',
    external_message_id  TEXT NOT NULL DEFAULT '',
    duration_ms          INTEGER NOT NULL DEFAULT 0
);

INSERT INTO publication_attempts_new SELECT * FROM publication_attempts;

DROP TABLE publication_attempts;
ALTER TABLE publication_attempts_new RENAME TO publication_attempts;

CREATE INDEX idx_attempts_outbox ON publication_attempts (outbox_id);
CREATE INDEX idx_attempts_attempted_at ON publication_attempts (attempted_at);
CREATE INDEX idx_attempts_outcome_attempted_at ON publication_attempts (outcome, attempted_at);
