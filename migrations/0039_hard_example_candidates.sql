-- migrations/0039_hard_example_candidates.sql
--
-- `train mine` scans stored
-- articles for cases where the promoted NB model and the LLM teacher
-- strongly disagree ("hard negative": NB confidently publish, LLM
-- confidently discard; "hard positive": the reverse). This table
-- persists those candidates so:
--   - domain/reviewpriority can boost their review priority
--     (HardMiningCount signal)
--   - application/retraintrigger's MinHardExampleCount trigger has a
--     real source (UnresolvedCount)
--   - a human resolving the article (any label) marks it resolved via
--     resolved_at, so it stops counting toward either signal
--
-- article_id is a natural primary key: re-mining an already-candidate
-- article upserts (refreshes scores), never duplicates. The FK is
-- ON DELETE CASCADE, matching every other table referencing
-- articles(id): foreign keys are enforced on every connection
-- (infra/sqlite/db.go), and usecase.RunRetention
-- purges articles with a single all-or-nothing DELETE that a NO ACTION
-- reference from here would abort. Candidates are fully reproducible by
-- re-running `train mine`, so cascading them away with their article
-- loses nothing.
--
-- Not added to sqlite.localOwnedTables (infra/sqlite/
-- merge.go): like editorial_shadow_comparisons and
-- editorial_label_proposals, this data is fully reproducible by
-- re-running `train mine`, not an irreplaceable human decision.
--
-- Rollback (operational -- this codebase has no down-migrations): this
-- migration is purely additive (one new table). To revert: `DROP TABLE
-- hard_example_candidates;` and delete this migration's row from
-- schema_migrations to let it reapply later.

CREATE TABLE hard_example_candidates (
    article_id     TEXT PRIMARY KEY REFERENCES articles(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('negative','positive')),
    nb_score       REAL NOT NULL,
    llm_label      TEXT NOT NULL,
    llm_confidence REAL NOT NULL,
    mined_at       TIMESTAMP NOT NULL,
    resolved_at    TIMESTAMP
);

CREATE INDEX idx_hard_example_candidates_unresolved ON hard_example_candidates (article_id) WHERE resolved_at IS NULL;
