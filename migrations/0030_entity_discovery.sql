-- migrations/0030_entity_discovery.sql
--
-- Storage for the offline `entities discover` command:
-- LLM-mined entity-catalog candidates awaiting human review, plus a
-- log of which articles have already been analyzed for a given
-- extractor version so repeat runs don't re-score the same article.

CREATE TABLE entity_proposals (
    id                TEXT PRIMARY KEY,
    canonical_name    TEXT NOT NULL,
    normalized_name   TEXT NOT NULL,
    entity_type       TEXT NOT NULL,
    kind              TEXT NOT NULL,
    target_entity_id  TEXT NOT NULL DEFAULT '',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    confidence        REAL NOT NULL DEFAULT 0,
    occurrences       INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'pending',
    first_seen        TIMESTAMP NOT NULL,
    last_seen         TIMESTAMP NOT NULL,
    extractor_version TEXT NOT NULL,
    created_at        TIMESTAMP NOT NULL,
    decided_at        TIMESTAMP
);

CREATE UNIQUE INDEX idx_entity_proposals_pending_identity
    ON entity_proposals (normalized_name, entity_type)
    WHERE status = 'pending';

CREATE TABLE entity_proposal_aliases (
    proposal_id TEXT NOT NULL REFERENCES entity_proposals(id) ON DELETE CASCADE,
    alias       TEXT NOT NULL,
    PRIMARY KEY (proposal_id, alias)
);

CREATE TABLE entity_proposal_sources (
    proposal_id   TEXT NOT NULL REFERENCES entity_proposals(id) ON DELETE CASCADE,
    article_id    TEXT NOT NULL,
    article_title TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (proposal_id, article_id)
);

CREATE TABLE entity_discovery_processed_articles (
    article_id        TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    processed_at      TIMESTAMP NOT NULL,
    PRIMARY KEY (article_id, extractor_version)
);
