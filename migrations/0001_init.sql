CREATE TABLE IF NOT EXISTS sources (
    id     TEXT PRIMARY KEY,
    name   TEXT NOT NULL,
    type   TEXT NOT NULL,
    domain TEXT NOT NULL DEFAULT '',
    url    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS articles (
    id            TEXT PRIMARY KEY,
    external_id   TEXT NOT NULL DEFAULT '',
    original_url  TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    fingerprint   TEXT NOT NULL,
    source_id     TEXT NOT NULL DEFAULT '',
    source_name   TEXT NOT NULL DEFAULT '',
    source_type   TEXT NOT NULL DEFAULT '',
    domain        TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    summary       TEXT NOT NULL DEFAULT '',
    author        TEXT NOT NULL DEFAULT '',
    language      TEXT NOT NULL DEFAULT '',
    published_at  TIMESTAMP,
    fetched_at    TIMESTAMP NOT NULL,
    content_type  TEXT NOT NULL DEFAULT '',
    metadata      TEXT NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL,
    decision      TEXT NOT NULL DEFAULT '',
    rejection     TEXT NOT NULL DEFAULT '',
    publication   TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_articles_fingerprint ON articles (fingerprint);
CREATE INDEX IF NOT EXISTS idx_articles_fetched_at ON articles (fetched_at);
