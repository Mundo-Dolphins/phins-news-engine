-- migrations/0046_outbox_destination.sql
--
-- Multi-destination outbox (ADR 0031). Each publication_outbox row now says
-- where it must be delivered, and the one-row-per-article rule becomes
-- one-row-per-(article, destination). Existing rows become 'telegram'.
--
-- SQLite cannot drop the inline UNIQUE(article_id) or add a CHECK in place,
-- so this rebuilds the table exactly like migration 0023: create the
-- replacement, copy every row (ids preserved, so publication_attempts,
-- event_cluster_publications.outbox_id and source_policy_cooldowns.outbox_id
-- stay valid), drop the original, rename, recreate every index.
-- infra/sqlite.Migrate disables foreign keys around the whole migration and
-- runs PRAGMA foreign_key_check afterwards, which is what makes the DROP
-- safe; do not run this file by hand against a connection with
-- foreign_keys=ON and data. It runs in one transaction recorded in
-- schema_migrations, so it is applied at most once and a failure rolls back
-- to the untouched table.
--
-- Columns carried over, in the shape migrations 0023/0038/0040 left them:
-- the full 0023 column set, expires_at (0038) and lane (0040). Indexes
-- recreated: 0023 idx_outbox_status_sent_at, 0038
-- idx_outbox_status_available_expires, 0040 idx_outbox_lane_status_available,
-- 0044 idx_outbox_status_created / idx_outbox_created /
-- idx_outbox_status_updated. idx_outbox_destination_status is new: it
-- serves the per-destination scoping of the Telegram counters and backlog.
--
-- Rollback: rebuild again without destination and with UNIQUE(article_id)
-- after deleting every row whose destination is not 'telegram'.
--
-- Adding a destination later means editing the CHECK list via another
-- rebuild; KnownDestinations in domain/publishing must match.

CREATE TABLE publication_outbox_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id        TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    destination       TEXT NOT NULL DEFAULT 'telegram' CHECK (destination IN ('telegram','bluesky')),
    payload           TEXT NOT NULL DEFAULT '{}',
    status            TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending','leased','sent_unconfirmed','delivered','retryable','quarantined','dead'
    )),
    available_at      TIMESTAMP NOT NULL,
    claimed_at        TIMESTAMP,
    claimed_by        TEXT NOT NULL DEFAULT '',
    attempt_count     INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT NOT NULL DEFAULT '',
    sent_message_id   TEXT NOT NULL DEFAULT '',
    sent_at           TIMESTAMP,
    ambiguous_reason  TEXT NOT NULL DEFAULT '',
    quarantined_at    TIMESTAMP,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at        TIMESTAMP,
    lane              TEXT NOT NULL DEFAULT 'normal' CHECK (lane IN ('normal','live')),
    UNIQUE (article_id, destination)
);

INSERT INTO publication_outbox_new (
    id, article_id, destination, payload, status, available_at, claimed_at, claimed_by,
    attempt_count, last_error, sent_message_id, sent_at, ambiguous_reason, quarantined_at,
    created_at, updated_at, expires_at, lane
)
SELECT
    id, article_id, 'telegram', payload, status, available_at, claimed_at, claimed_by,
    attempt_count, last_error, sent_message_id, sent_at, ambiguous_reason, quarantined_at,
    created_at, updated_at, expires_at, lane
FROM publication_outbox;

DROP TABLE publication_outbox;
ALTER TABLE publication_outbox_new RENAME TO publication_outbox;

CREATE INDEX idx_outbox_status_sent_at ON publication_outbox (status, sent_at);
CREATE INDEX idx_outbox_status_available_expires
    ON publication_outbox (status, available_at, expires_at);
CREATE INDEX idx_outbox_lane_status_available
    ON publication_outbox (lane, status, available_at, expires_at);
CREATE INDEX idx_outbox_status_created ON publication_outbox (status, created_at);
CREATE INDEX idx_outbox_created ON publication_outbox (created_at);
CREATE INDEX idx_outbox_status_updated ON publication_outbox (status, updated_at);
CREATE INDEX idx_outbox_destination_status ON publication_outbox (destination, status);
