-- migrations/0029_editorial_decision_metadata.sql
--
-- Adds the editorial-gate audit fields required on every
-- persisted decision: which mode was active, which model artifact
-- (by checksum) produced the score, and the reject threshold that was
-- applied alongside the already-existing accept threshold (`threshold`
-- column, unchanged meaning). All three are nullable-safe with
-- non-null defaults so existing rows (written before this migration)
-- read back as empty string / 0 rather than NULL — no backfill
-- attempted, since the mode/model/threshold that produced a historical
-- row is not reconstructible from data alone.
ALTER TABLE article_processing_events ADD COLUMN mode TEXT NOT NULL DEFAULT '';
ALTER TABLE article_processing_events ADD COLUMN model_checksum TEXT NOT NULL DEFAULT '';
ALTER TABLE article_processing_events ADD COLUMN applied_reject_threshold REAL NOT NULL DEFAULT 0;
