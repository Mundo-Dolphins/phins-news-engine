-- migrations/0042_knowledge_cache.sql
--
-- Cache for the offline structured-knowledge source (Wikidata, issue
-- #314): search results keyed by sha256(provider|language|limit|query)
-- and entity records keyed by (provider, language, entity_id). Negative
-- rows (empty search / missing entity) are stored with negative = 1 and a
-- shorter TTL. Timestamps are TEXT in a fixed-width UTC format so
-- `expires_at < ?` compares correctly as a string (and so the driver
-- never rewrites them into time.Time).

CREATE TABLE knowledge_search_cache (
    query_hash   TEXT PRIMARY KEY,
    provider     TEXT NOT NULL,
    language     TEXT NOT NULL,
    limit_n      INTEGER NOT NULL,
    query        TEXT NOT NULL,
    results_json TEXT NOT NULL,
    negative     INTEGER NOT NULL DEFAULT 0 CHECK (negative IN (0, 1)),
    searched_at  TEXT NOT NULL,
    expires_at   TEXT NOT NULL
);

CREATE INDEX idx_knowledge_search_cache_expires ON knowledge_search_cache (expires_at);

CREATE TABLE knowledge_entity_cache (
    provider    TEXT NOT NULL,
    language    TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    entity_json TEXT NOT NULL,
    negative    INTEGER NOT NULL DEFAULT 0 CHECK (negative IN (0, 1)),
    searched_at TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    PRIMARY KEY (provider, language, entity_id)
);

CREATE INDEX idx_knowledge_entity_cache_expires ON knowledge_entity_cache (expires_at);
