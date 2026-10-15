CREATE TABLE IF NOT EXISTS seen_items (
    id                  TEXT PRIMARY KEY,
    platform            TEXT NOT NULL,
    source_id           TEXT NOT NULL,
    identity_basis      TEXT NOT NULL,
    identity_key        TEXT NOT NULL,
    identity_algorithm  TEXT NOT NULL,
    -- DEFERRABLE INITIALLY DEFERRED (unlike every other article_id FK in
    -- this schema): ProcessCycle's claim step deliberately inserts this
    -- row *before* the parent articles row exists yet, within the same
    -- transaction (that ordering is what lets an already-seen item skip
    -- the pipeline without ever writing an articles row at all) — the
    -- parent-first Save that follows for a genuinely new claim still runs
    -- before COMMIT, so the constraint is satisfied by the time SQLite
    -- actually checks it.
    article_id          TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    canonical_url       TEXT NOT NULL DEFAULT '',
    first_seen_at       TIMESTAMP NOT NULL,
    last_seen_at        TIMESTAMP NOT NULL,
    seen_count          INTEGER NOT NULL DEFAULT 1,
    terminal_outcome    TEXT NOT NULL DEFAULT '',
    content_hash        TEXT NOT NULL DEFAULT '',
    revision            INTEGER NOT NULL DEFAULT 1,
    reprocessed_count   INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_seen_items_identity ON seen_items (platform, source_id, identity_key);
CREATE INDEX IF NOT EXISTS idx_seen_items_article ON seen_items (article_id);
