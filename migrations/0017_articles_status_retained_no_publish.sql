-- migrations/0017_articles_status_retained_no_publish.sql
--
-- Adds 'retained_no_publish' to articles.status's CHECK constraint.
--
-- Bug: article.StatusRetainedNoPublish ("retained_no_publish", Phase 12) has been a valid domain status since migration 0010 added
-- the source-policy columns it depends on, and ProcessCycle.retainNoPublish
-- (Gate B) has saved articles with that status ever since — but the
-- CHECK constraint this table has carried since migration 0002 never
-- listed it. Every real save with that status has always violated the
-- CHECK constraint and failed; every test that exercised Gate B's
-- retain-without-publish outcome used an in-memory fake repository, which
-- does not enforce CHECK constraints, so this went unnoticed until a
-- real-SQLite integration test (added alongside the fix) hit it.
--
-- SQLite has no ALTER TABLE support for adding or dropping a CHECK
-- constraint, so this rebuilds the table: create a replacement with the
-- corrected constraint, copy every row across unchanged, drop the
-- original, rename the replacement into place, then recreate its indexes.
-- All existing columns, defaults, and foreign keys are preserved exactly
-- as migrations 0001-0014 left them (see the CREATE TABLE below); nothing
-- about the schema changes except the one added CHECK value.
--
-- Foreign key safety: `articles` is the parent of several ON DELETE
-- CASCADE/SET NULL child tables (article_aliases,
-- article_processing_events, exact_fingerprints, source_policy_decisions,
-- event_cluster_members, and more). With foreign key enforcement on, DROP
-- TABLE performs an implicit DELETE of every row first, which would fire
-- those cascades and silently wipe every child table. This migration
-- relies on infrastructure/sqlite.Migrate's applyMigration disabling
-- foreign key enforcement around the whole operation (and running
-- PRAGMA foreign_key_check immediately after) for exactly this reason —
-- see that function's doc comment. Do not run this file's SQL directly
-- against a connection with foreign_keys=ON and existing data.
--
-- Compatibility: purely additive — every row's existing status value is
-- still valid under the new constraint, and no column, index, or foreign
-- key changes shape or name. Older code that never sets
-- 'retained_no_publish' is unaffected.
--
-- Rollback: to revert, rebuild the table again with the pre-0017 CHECK
-- constraint (migration 0002's list, without 'retained_no_publish'), the
-- same way this migration rebuilds it — but only after confirming no row
-- actually has status = 'retained_no_publish' (any that do would violate
-- the restored constraint and must be reassigned or removed first, e.g.
-- back to 'rejected_duplicate', the closest pre-Phase-12 equivalent).
CREATE TABLE articles_new (
    id                     TEXT PRIMARY KEY,
    external_id            TEXT NOT NULL DEFAULT '',
    original_url           TEXT NOT NULL,
    canonical_url          TEXT NOT NULL,
    source_id              TEXT NOT NULL DEFAULT '',
    source_name            TEXT NOT NULL DEFAULT '',
    source_type            TEXT NOT NULL DEFAULT '',
    domain                 TEXT NOT NULL DEFAULT '',
    title                  TEXT NOT NULL DEFAULT '',
    summary                TEXT NOT NULL DEFAULT '',
    author                 TEXT NOT NULL DEFAULT '',
    language               TEXT NOT NULL DEFAULT '',
    published_at           TIMESTAMP,
    fetched_at             TIMESTAMP NOT NULL,
    content_type           TEXT NOT NULL DEFAULT '',
    metadata               TEXT NOT NULL DEFAULT '{}',
    status                 TEXT NOT NULL CHECK (status IN (
        'fetched','normalized','rejected_validation','rejected_duplicate',
        'rejected_not_related','cluster_candidate','clustered',
        'rejected_quality','rejected_stale','pending_publication',
        'publishing','published','publish_failed','pending_retry','discarded',
        'retained_no_publish'
    )),
    status_reason           TEXT NOT NULL DEFAULT '',
    editorial_score         REAL NOT NULL DEFAULT 0,
    quality_threshold_used  REAL NOT NULL DEFAULT 0,
    freshness_max_age_used  INTEGER NOT NULL DEFAULT 0,
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    current_cluster_id      TEXT REFERENCES event_clusters(id) ON DELETE SET NULL,
    classifier_version      TEXT REFERENCES model_registry(version) DEFAULT 'placeholder-v0',
    dictionary_version      TEXT REFERENCES dictionary_registry(version) DEFAULT 'placeholder-v0',
    relevance_score         REAL NOT NULL DEFAULT 0,
    primary_entity          TEXT NOT NULL DEFAULT '',
    cluster_member_role     TEXT NOT NULL DEFAULT '',
    is_trusted_source       INTEGER NOT NULL DEFAULT 0
);

INSERT INTO articles_new SELECT * FROM articles;

DROP TABLE articles;
ALTER TABLE articles_new RENAME TO articles;

CREATE INDEX idx_articles_status ON articles (status);
CREATE INDEX idx_articles_fetched_at ON articles (fetched_at);
CREATE INDEX idx_articles_status_published_at ON articles (status, published_at);
CREATE INDEX idx_articles_status_entity_published_at ON articles (status, primary_entity, published_at);
CREATE INDEX idx_articles_status_fetched_at ON articles (status, fetched_at);
CREATE INDEX idx_articles_current_cluster_id ON articles (current_cluster_id);
CREATE INDEX idx_articles_canonical_url ON articles (canonical_url);
