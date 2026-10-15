-- migrations/0031_unknown_entity_occurrences.sql
--
-- Raw, append-only evidence of name-like spans the deterministic
-- extractor (domain/eventsignals) could not match to
-- any catalog entity. One row per occurrence, no
-- aggregation at write time — a later sub-project (LLM fallback / offline
-- discovery) aggregates by normalized_text as needed, mirroring how
-- entity_proposals (migration 0030) already aggregates a different
-- evidence stream.

CREATE TABLE unknown_entity_occurrences (
    id              TEXT PRIMARY KEY,
    article_id      TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    candidate_text  TEXT NOT NULL,
    normalized_text TEXT NOT NULL,
    possible_type   TEXT NOT NULL DEFAULT '',
    confidence      REAL NOT NULL DEFAULT 0,
    reason          TEXT NOT NULL DEFAULT '',
    field           TEXT NOT NULL DEFAULT '',
    rules_version   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL
);

CREATE INDEX idx_unknown_entity_occurrences_normalized_text
    ON unknown_entity_occurrences (normalized_text);
