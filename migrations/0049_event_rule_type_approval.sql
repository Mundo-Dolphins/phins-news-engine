-- migrations/0049_event_rule_type_approval.sql
--
-- Generated event types: an approved type proposal now carries the operator's
-- choices that become its entry in the generated rules file: the match window
-- (a Go duration string, '48h' by default) and whether the type is
-- referential. They are written when the proposal is approved and read when it
-- is applied; rows from before this migration read back as '48h' and 0.
--
-- event_rule_proposals is a locally-owned table (see sqlite.localOwnedTables):
-- the sync copies it by explicit column names, so a previous local database
-- that predates these columns still merges.
--
-- Rollback (operational -- this codebase has no down-migrations): purely
-- additive. To revert: `ALTER TABLE event_rule_proposals DROP COLUMN type_window;`
-- and `ALTER TABLE event_rule_proposals DROP COLUMN type_referential;`, then
-- delete this migration's row from schema_migrations to let it reapply later.

ALTER TABLE event_rule_proposals ADD COLUMN type_window TEXT NOT NULL DEFAULT '48h';
ALTER TABLE event_rule_proposals ADD COLUMN type_referential INTEGER NOT NULL DEFAULT 0;
