ALTER TABLE human_labels ADD COLUMN reason TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_articles_status_fetched_at ON articles (status, fetched_at);
CREATE INDEX idx_articles_current_cluster_id ON articles (current_cluster_id);

CREATE TABLE editorial_cycle_sessions (
    id            TEXT PRIMARY KEY,
    started_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','abandoned')),
    current_step  TEXT NOT NULL DEFAULT 'review',
    flags_json    TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_editorial_cycle_sessions_status ON editorial_cycle_sessions (status);

CREATE TABLE editorial_review_decisions (
    session_id  TEXT NOT NULL REFERENCES editorial_cycle_sessions(id) ON DELETE CASCADE,
    article_id  TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    action      TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    notes       TEXT NOT NULL DEFAULT '',
    decided_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (session_id, article_id)
);
CREATE INDEX idx_review_decisions_session ON editorial_review_decisions (session_id);
CREATE INDEX idx_review_decisions_action_decided_at ON editorial_review_decisions (action, decided_at);
