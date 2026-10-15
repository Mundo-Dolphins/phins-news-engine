CREATE TABLE articles_new (
    id                     TEXT PRIMARY KEY,
    external_id            TEXT NOT NULL DEFAULT '',
    original_url           TEXT NOT NULL,
    canonical_url          TEXT NOT NULL,
    source_id              TEXT NOT NULL DEFAULT '',
    source_name            TEXT NOT NULL DEFAULT '',
    source_type            TEXT NOT NULL DEFAULT '',
    domain                 TEXT NOT NULL DEFAULT '',
    title                  TEXT NOT NULL DEFAULT '',
    summary                TEXT NOT NULL DEFAULT '',
    author                 TEXT NOT NULL DEFAULT '',
    language                TEXT NOT NULL DEFAULT '',
    published_at            TIMESTAMP,
    fetched_at               TIMESTAMP NOT NULL,
    content_type             TEXT NOT NULL DEFAULT '',
    metadata                 TEXT NOT NULL DEFAULT '{}',
    status                   TEXT NOT NULL CHECK (status IN (
        'fetched','normalized','rejected_validation','rejected_duplicate',
        'rejected_not_related','cluster_candidate','clustered',
        'rejected_quality','rejected_stale','pending_publication',
        'publishing','published','publish_failed','pending_retry','discarded'
    )),
    status_reason            TEXT NOT NULL DEFAULT '',
    editorial_score          REAL NOT NULL DEFAULT 0,
    quality_threshold_used   REAL NOT NULL DEFAULT 0,
    freshness_max_age_used   INTEGER NOT NULL DEFAULT 0,
    created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO articles_new (
    id, external_id, original_url, canonical_url, source_id, source_name,
    source_type, domain, title, summary, author, language, published_at,
    fetched_at, content_type, metadata, status, status_reason
)
SELECT
    id, external_id, original_url, canonical_url, source_id, source_name,
    source_type, domain, title, summary, author, language, published_at,
    fetched_at, content_type, metadata,
    CASE status
        WHEN 'published' THEN 'published'
        WHEN 'rejected' THEN 'rejected_stale'
        WHEN 'processed' THEN 'pending_publication'
        ELSE 'fetched'
    END,
    ''
FROM articles;

DROP TABLE articles;
ALTER TABLE articles_new RENAME TO articles;

CREATE INDEX idx_articles_status ON articles (status);
CREATE INDEX idx_articles_fetched_at ON articles (fetched_at);

CREATE TABLE article_aliases (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id    TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    alias_type    TEXT NOT NULL,
    alias_value   TEXT NOT NULL,
    first_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_article_aliases_value ON article_aliases (alias_type, alias_value);
CREATE INDEX idx_article_aliases_article ON article_aliases (article_id);

CREATE TABLE article_processing_events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id          TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    occurred_at         TIMESTAMP NOT NULL,
    from_status         TEXT NOT NULL DEFAULT '',
    to_status           TEXT NOT NULL,
    event_type          TEXT NOT NULL,
    reason_code         TEXT NOT NULL DEFAULT '',
    reason_detail       TEXT NOT NULL DEFAULT '{}',
    classifier_version  TEXT NOT NULL DEFAULT '',
    dictionary_version  TEXT NOT NULL DEFAULT '',
    config_snapshot     TEXT NOT NULL DEFAULT '{}',
    score               REAL NOT NULL DEFAULT 0,
    threshold           REAL NOT NULL DEFAULT 0,
    cycle_id            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_events_article ON article_processing_events (article_id);
CREATE INDEX idx_events_occurred_at ON article_processing_events (occurred_at);

CREATE TABLE exact_fingerprints (
    fingerprint  TEXT PRIMARY KEY,
    article_id   TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_fingerprints_article ON exact_fingerprints (article_id);
