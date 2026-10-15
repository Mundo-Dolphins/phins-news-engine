-- migrations/0034_web_search_cache.sql
--
-- Storage for the offline catalog-verification web-lookup tool loop:
-- a cache of web-search results keyed by
-- sha256(provider + normalized query), so the same query against the
-- same provider never triggers a duplicate call within a cache_ttl
-- window (across or within a single `entities discover --verify` run);
-- and a join table recording which trusted-domain web sources backed a
-- given entity_proposals row, mirroring entity_proposal_sources'
-- (proposal_id, ...) join-table shape rather than a JSON column.

CREATE TABLE web_search_cache (
    query_hash   TEXT PRIMARY KEY,
    provider     TEXT NOT NULL,
    query        TEXT NOT NULL,
    results_json TEXT NOT NULL,
    searched_at  TIMESTAMP NOT NULL,
    expires_at   TIMESTAMP NOT NULL
);

CREATE TABLE entity_proposal_web_sources (
    proposal_id TEXT NOT NULL REFERENCES entity_proposals(id) ON DELETE CASCADE,
    domain      TEXT NOT NULL,
    url         TEXT NOT NULL,
    title       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (proposal_id, url)
);
