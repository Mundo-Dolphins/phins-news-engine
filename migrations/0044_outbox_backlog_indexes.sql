-- migrations/0044_outbox_backlog_indexes.sql
--
-- Index support for the read-only backlog snapshot
-- (OutboxRepository.BacklogSnapshot / `telegram outbox backlog`):
--   * (status, created_at): oldest claimable row, i.e. queue age.
--   * (created_at): rows enqueued inside a window.
--   * (status, updated_at): rows that became dead (expired/superseded)
--     inside a window.
CREATE INDEX IF NOT EXISTS idx_outbox_status_created ON publication_outbox (status, created_at);
CREATE INDEX IF NOT EXISTS idx_outbox_created ON publication_outbox (created_at);
CREATE INDEX IF NOT EXISTS idx_outbox_status_updated ON publication_outbox (status, updated_at);
