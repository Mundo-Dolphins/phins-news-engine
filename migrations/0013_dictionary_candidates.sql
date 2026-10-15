CREATE TABLE dictionary_candidate_terms (
    term                TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL REFERENCES editorial_cycle_sessions(id),
    category_guess      TEXT NOT NULL DEFAULT '',
    support_count       INTEGER NOT NULL,
    first_seen_article  TEXT NOT NULL REFERENCES articles(id),
    status              TEXT NOT NULL DEFAULT 'pending',
    decided_at          TIMESTAMP
);
CREATE INDEX idx_dictionary_candidate_terms_session ON dictionary_candidate_terms (session_id);
