-- migrations/0036_review_priority_scoring.sql
--
-- Storage for deterministic review-priority
-- scoring. Adds first_seen_at to sources (the "new source" signal has
-- no data to read without it) and review_queue, the live "worth a
-- human's time" queue editorial classify populates after
-- saving each proposal and `train review` drains.
--
-- review_queue has no FOREIGN KEY on article_id/proposal_id, matching
-- editorial_label_proposals' (migration 0035) convention for
-- offline/reproducible proposal-adjacent data: it is fully
-- recomputable by re-running the sync step, so a missing FK to allow
-- cheap upserts is an accepted tradeoff, same reasoning as 0035.
--
-- Rollback (operational -- this codebase has no down-migrations): this
-- migration is purely additive (one new column, one new table). To
-- revert: `ALTER TABLE sources DROP COLUMN first_seen_at; DROP TABLE
-- review_queue;` and delete this migration's row from
-- schema_migrations to let it reapply later.

ALTER TABLE sources ADD COLUMN first_seen_at TIMESTAMP;

UPDATE sources
SET first_seen_at = COALESCE(
    (SELECT MIN(a.fetched_at) FROM articles a WHERE a.source_id = sources.id),
    CURRENT_TIMESTAMP
)
WHERE first_seen_at IS NULL;

CREATE TABLE review_queue (
    article_id      TEXT PRIMARY KEY,
    proposal_id     TEXT NOT NULL,
    score           REAL NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    nb_zone         TEXT NOT NULL,
    nb_value        REAL NOT NULL,
    llm_label       TEXT NOT NULL,
    llm_confidence  REAL NOT NULL,
    suggested_label TEXT NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    updated_at      TIMESTAMP NOT NULL
);
