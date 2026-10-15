-- migrations/0038_training_runs.sql
--
-- `train run` persists one row per invocation
-- so past runs are queryable later, and so sub-project B's retrain-trigger
-- checker can compute deltas ("new Gold/Silver examples since the last
-- run") via Latest(). started_at/finished_at/status track the run's own
-- lifecycle; report holds the full rendered textual report (see
-- application/trainreport) verbatim.
--
-- The `training_runs` table itself already exists (migration 0005,
-- 0005_registries.sql) and is already listed in sqlite.registryTables
-- (infra/sqlite/merge.go), alongside model_registry
-- and dictionary_registry: rows are natural-keyed (id) and unioned
-- (INSERT OR IGNORE) from the previous local database into a freshly
-- promoted snapshot on sync, rather than deleted-and-replaced wholesale
-- the way sqlite.localOwnedTables (e.g. human_labels, dictionary_history)
-- is (editorial_shadow_comparisons, migration 0037, is a different case:
-- its own comment says it was deliberately left out of localOwnedTables
-- entirely, since a sync losing it is acceptable). No change to
-- registryTables is needed here. That 0005 schema was laid down
-- speculatively and never written to by application code until this
-- task's repository (Save/Latest); this migration only adds the one
-- column (report) the run history needs plus a lookup index.
--
-- Rollback (operational -- this codebase has no down-migrations): this
-- migration is purely additive (one new column, one new index) to a
-- pre-existing table. To revert: `DROP INDEX idx_training_runs_started_at;`
-- (SQLite has no DROP COLUMN pre-3.35; recreate the table without
-- `report` if a true rollback is ever needed) and delete this
-- migration's row from schema_migrations to let it reapply later.

ALTER TABLE training_runs ADD COLUMN report TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_training_runs_started_at ON training_runs (started_at);
