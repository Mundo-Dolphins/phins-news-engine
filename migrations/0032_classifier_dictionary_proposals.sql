-- migrations/0032_classifier_dictionary_proposals.sql
--
-- Offline, review-gated proposals for changes to the team relevance
-- classifier dictionary. Kept separate from entity_proposals because these
-- rows target dictionary categories/operations, not entity-catalog types.

CREATE TABLE classifier_dictionary_proposals (
    id                TEXT PRIMARY KEY,
    kind              TEXT NOT NULL,
    category          TEXT NOT NULL DEFAULT '',
    value             TEXT NOT NULL,
    normalized_value  TEXT NOT NULL,
    operation         TEXT NOT NULL DEFAULT 'add',
    target_entry      TEXT NOT NULL DEFAULT '',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    confidence        REAL NOT NULL DEFAULT 0,
    occurrences       INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'pending',
    first_seen        TIMESTAMP NOT NULL,
    last_seen         TIMESTAMP NOT NULL,
    analyzer_version  TEXT NOT NULL,
    created_at        TIMESTAMP NOT NULL,
    decided_at        TIMESTAMP
);

CREATE UNIQUE INDEX idx_classifier_dictionary_proposals_pending_identity
    ON classifier_dictionary_proposals (normalized_value, kind, category, operation)
    WHERE status = 'pending';

CREATE TABLE classifier_dictionary_proposal_sources (
    proposal_id   TEXT NOT NULL REFERENCES classifier_dictionary_proposals(id) ON DELETE CASCADE,
    article_id    TEXT NOT NULL,
    article_title TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (proposal_id, article_id)
);

CREATE TABLE classifier_dictionary_analysis_processed_articles (
    article_id        TEXT NOT NULL,
    analyzer_version  TEXT NOT NULL,
    processed_at      TIMESTAMP NOT NULL,
    PRIMARY KEY (article_id, analyzer_version)
);

