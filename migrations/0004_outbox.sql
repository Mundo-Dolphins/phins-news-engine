CREATE TABLE publication_outbox (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id    TEXT NOT NULL UNIQUE REFERENCES articles(id) ON DELETE CASCADE,
    payload       TEXT NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','claimed','published','dead')),
    available_at  TIMESTAMP NOT NULL,
    claimed_at    TIMESTAMP,
    claimed_by    TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_outbox_status_available ON publication_outbox (status, available_at);

CREATE TABLE publication_attempts (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    outbox_id           INTEGER NOT NULL REFERENCES publication_outbox(id) ON DELETE CASCADE,
    article_id          TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    attempted_at         TIMESTAMP NOT NULL,
    outcome              TEXT NOT NULL CHECK (outcome IN ('succeeded','temporary_failure','permanent_failure')),
    error_message        TEXT NOT NULL DEFAULT '',
    external_message_id  TEXT NOT NULL DEFAULT '',
    duration_ms          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_attempts_outbox ON publication_attempts (outbox_id);
CREATE INDEX idx_attempts_attempted_at ON publication_attempts (attempted_at);
