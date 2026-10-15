-- migrations/0035_editorial_label_proposals.sql
--
-- Storage foundation for the offline LLM
-- "teacher" workflow. This migration adds no LLM calls, no CLI, no
-- retraining -- it only creates the tables that the independent LLM
-- classification will write into and the review CLI will read
-- from and update.
--
-- Deliberately NOT a change to human_labels/editorial_examples: those
-- tables are exclusively human-authored (HumanLabelRepository.Label/
-- Relabel/Retract) and enforce at most one *active* row per article_id
-- (idx_human_labels_one_active / idx_editorial_examples_one_active).
-- Reusing them for machine-suggested labels would conflict with that
-- invariant and blur "human decided this" with "the LLM suggested
-- this" -- exactly the distinction the design requires ("manual
-- decision > LLM suggestion", never silently overwritten).
--
-- Instead this mirrors the existing proposal-table shape already used
-- for other offline, review-gated machine output: entity_proposals
-- (migration 0030) and classifier_dictionary_proposals (migration
-- 0032) -- a `..._proposals` table with a `status` column plus a
-- `..._processed_articles` table so a batch classification run can
-- skip articles it already analyzed under the current prompt_version.
--
-- editorial_label_proposals has no sources/aggregation table like
-- classifier_dictionary_proposals: each row is one LLM classification
-- of exactly one article, never aggregated evidence across many
-- articles, so there is nothing to merge on repeat evidence. A new
-- offline run may only add a fresh proposal for an article once its
-- prior proposal has left status='pending' (see the partial unique
-- index below) -- this mirrors entity_proposals'/classifier_dictionary_
-- proposals' identical constraint.
--
-- Not added to sqlite.localOwnedTables (infra/sqlite/
-- merge.go): like entity_proposals and classifier_dictionary_proposals,
-- this data is fully reproducible by re-running the offline classifier,
-- unlike human_labels/editorial_examples which encode irreplaceable
-- human decisions.
--
-- Rollback (operational -- this codebase has no down-migrations): this
-- migration is purely additive and touches no existing table. To
-- revert: `DROP TABLE editorial_label_proposals; DROP TABLE
-- editorial_label_analysis_processed_articles;` and delete this
-- migration's row from schema_migrations to let it reapply later.

CREATE TABLE editorial_label_proposals (
    id              TEXT PRIMARY KEY,
    article_id      TEXT NOT NULL,
    label           TEXT NOT NULL,
    confidence      REAL NOT NULL DEFAULT 0,
    reason          TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL,
    prompt_version  TEXT NOT NULL,
    policy_version  TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMP NOT NULL,
    decided_at      TIMESTAMP
);

CREATE INDEX idx_editorial_label_proposals_article ON editorial_label_proposals (article_id);

CREATE UNIQUE INDEX idx_editorial_label_proposals_pending_identity
    ON editorial_label_proposals (article_id)
    WHERE status = 'pending';

CREATE TABLE editorial_label_analysis_processed_articles (
    article_id      TEXT NOT NULL,
    prompt_version  TEXT NOT NULL,
    processed_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (article_id, prompt_version)
);
