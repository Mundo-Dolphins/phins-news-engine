CREATE TABLE model_registry (
    version      TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    path         TEXT NOT NULL DEFAULT '',
    trained_at   TIMESTAMP,
    promoted_at  TIMESTAMP,
    metrics      TEXT NOT NULL DEFAULT '{}',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE dictionary_registry (
    version      TEXT PRIMARY KEY,
    path         TEXT NOT NULL DEFAULT '',
    entry_count  INTEGER NOT NULL DEFAULT 0,
    promoted_at  TIMESTAMP,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE human_labels (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id  TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    labeled_by  TEXT NOT NULL DEFAULT '',
    labeled_at  TIMESTAMP NOT NULL,
    notes       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_human_labels_article ON human_labels (article_id);

CREATE TABLE training_runs (
    id               TEXT PRIMARY KEY,
    started_at       TIMESTAMP NOT NULL,
    finished_at      TIMESTAMP,
    model_version    TEXT REFERENCES model_registry(version),
    dataset_summary  TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed')),
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO model_registry (version, kind, path, metrics) VALUES ('placeholder-v0', 'editorial-classifier', '', '{}');
INSERT INTO dictionary_registry (version, path, entry_count) VALUES ('placeholder-v0', '', 0);

ALTER TABLE articles ADD COLUMN classifier_version TEXT REFERENCES model_registry(version) DEFAULT 'placeholder-v0';
ALTER TABLE articles ADD COLUMN dictionary_version TEXT REFERENCES dictionary_registry(version) DEFAULT 'placeholder-v0';
