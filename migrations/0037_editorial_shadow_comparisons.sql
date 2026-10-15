-- migrations/0037_editorial_shadow_comparisons.sql
--
-- Offline shadow comparison between
-- the production editorial model and a candidate, run entirely against
-- already-stored articles (no live traffic). Only disagreements
-- (production Zone != candidate Zone) are stored -- agreements are
-- summarized in the CLI's printed report but not persisted, since a
-- human reviewing shadow-comparison output only ever needs to see where
-- the two models diverge.
--
-- Not added to sqlite.localOwnedTables (infra/sqlite/
-- merge.go): like editorial_label_proposals (migration 0035) and
-- classifier_dictionary_proposals (migration 0032), this data is fully
-- reproducible by re-running `train shadow-compare`, unlike
-- human_labels/editorial_examples which encode irreplaceable human
-- decisions.
--
-- Rollback (operational -- this codebase has no down-migrations): this
-- migration is purely additive (one new table). To revert: `DROP TABLE
-- editorial_shadow_comparisons;` and delete this migration's row from
-- schema_migrations to let it reapply later.

CREATE TABLE editorial_shadow_comparisons (
    run_id             TEXT NOT NULL,
    article_id         TEXT NOT NULL,
    title              TEXT NOT NULL,
    production_version TEXT NOT NULL,
    production_zone    TEXT NOT NULL,
    production_score   REAL NOT NULL,
    candidate_version  TEXT NOT NULL,
    candidate_zone     TEXT NOT NULL,
    candidate_score    REAL NOT NULL,
    created_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (run_id, article_id)
);

CREATE INDEX idx_editorial_shadow_comparisons_run ON editorial_shadow_comparisons (run_id);
