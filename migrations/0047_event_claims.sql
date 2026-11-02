-- migrations/0047_event_claims.sql
--
-- Event claims (ADR 0033, ADR 0034): a transactional idempotency store for
-- event handlers. A handler reserves a unique key (for example "one injury
-- report per day") before it acts, and the UNIQUE (handler, claim_key)
-- constraint makes the database, not the handler, the arbiter when two
-- articles race for the same key.
--
-- The claim is written by the handler inside the article's transaction, in
-- the same transaction as its outbox row, so a rolled-back article leaves no
-- claim behind. article_id deliberately has no foreign key: claims are kept
-- as a record of what was posted even if the article row is later deleted.
-- data holds handler-specific JSON (week, report date, ordinal, rival,
-- image path, extracted names) and is opaque to the engine.
--
-- State machine: claimed -> published | failed. A failed claim can be
-- released (deleted) by an operator so the key can be claimed again.
--
-- Production writes this table, so it is a normal merged table, not one of
-- sqlite.localOwnedTables.
--
-- Rollback (operational -- this codebase has no down-migrations): purely
-- additive. To revert: `DROP TABLE event_claims;` and delete this
-- migration's row from schema_migrations to let it reapply later.

CREATE TABLE event_claims (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    handler    TEXT NOT NULL,
    claim_key  TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'claimed' CHECK (state IN ('claimed','published','failed')),
    article_id TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (handler, claim_key)
);
-- Serves the reconcile step, which looks up open claims by article.
CREATE INDEX idx_event_claims_article ON event_claims (article_id) WHERE article_id <> '';
CREATE INDEX idx_event_claims_state ON event_claims (state, handler);
