-- Persist the freshness decision made when an article enters the outbox.
-- NULL is intentional: legacy rows with an unknown published_at or a missing
-- freshness window cannot be assigned a defensible absolute expiry. They are
-- retained for manual inspection and are never expired by the automatic claim
-- predicate.
ALTER TABLE publication_outbox ADD COLUMN expires_at TIMESTAMP;

-- Deterministic backfill. It uses only values captured on the article and
-- never CURRENT_TIMESTAMP: an old publication therefore cannot gain a new
-- life merely because this migration was run today. Unknown published_at or
-- freshness_max_age_used (zero) stays NULL and is handled by the bounded,
-- auditable operator sweep rather than by automatic claiming.
UPDATE publication_outbox
SET expires_at = (
    SELECT strftime('%Y-%m-%dT%H:%M:%fZ', a.published_at, printf('+%d seconds', a.freshness_max_age_used))
    FROM articles a
    WHERE a.id = publication_outbox.article_id
      AND a.published_at IS NOT NULL
      AND a.freshness_max_age_used > 0
)
WHERE expires_at IS NULL;

-- This is the access path used by ClaimBatch and the bounded operator sweep.
-- The expiry column is last because available_at remains the queue's due-time
-- ordering key; NULL expiry values sort after known values in the explicit
-- predicates used by those queries.
DROP INDEX IF EXISTS idx_outbox_status_available;
CREATE INDEX IF NOT EXISTS idx_outbox_status_available_expires
    ON publication_outbox (status, available_at, expires_at);
