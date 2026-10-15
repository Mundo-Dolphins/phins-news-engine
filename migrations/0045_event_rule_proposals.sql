-- migrations/0045_event_rule_proposals.sql
--
-- Event-rule discovery ("event type and rule
-- discovery in the editorial cycle"): storage for LLM-proposed event rules
-- (new trigger phrases or new event types) awaiting operator review.
--
-- Mirrors the shape of entity_proposals (migration 0030): an offline,
-- review-gated queue. The rows carry the operator's decisions (approved,
-- rejected, applied), which are not reproducible, and production never
-- writes the table, so it is listed in sqlite.localOwnedTables: a sync
-- replaces the snapshot's empty copy with the previous local rows.
--
-- The unique index covers every status: there is one row per logical rule,
-- so a rejected proposal is never resurrected by a re-run (the repository
-- returns the stored row unchanged instead of inserting a duplicate).
--
-- Rollback (operational -- this codebase has no down-migrations): purely
-- additive. To revert: `DROP TABLE event_rule_proposals;` and delete this
-- migration's row from schema_migrations to let it reapply later.

CREATE TABLE event_rule_proposals (
    id             TEXT PRIMARY KEY,
    kind           TEXT NOT NULL CHECK (kind IN ('phrase','type')),
    target_kind    TEXT NOT NULL DEFAULT '',
    target         TEXT NOT NULL DEFAULT '',
    phrase         TEXT NOT NULL DEFAULT '',
    type_name      TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    evidence_json  TEXT NOT NULL DEFAULT '[]',
    status         TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','applied')),
    prompt_version TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL,
    decided_at     TIMESTAMP
);
-- One live (non-rejected) proposal per logical rule; a rejected one is never resurrected by a re-run.
CREATE UNIQUE INDEX idx_event_rule_proposals_key
    ON event_rule_proposals (kind, target_kind, target, lower(phrase), lower(type_name));
