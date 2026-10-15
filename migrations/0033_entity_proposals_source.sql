-- migrations/0033_entity_proposals_source.sql
--
-- Distinguishes which pipeline produced an entity_proposals row: the
-- offline batch-mining `entities discover` command vs the online, per-candidate resolution fallback that
-- runs inside ingestion. Every existing row is
-- 'discovery' by construction (no backfill needed — that's what it always
-- was), which is exactly the column default.

ALTER TABLE entity_proposals ADD COLUMN source TEXT NOT NULL DEFAULT 'discovery';
