CREATE TABLE dictionary_history (
    version             TEXT PRIMARY KEY,
    content_hash        TEXT NOT NULL,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    change_count        INTEGER NOT NULL DEFAULT 0,
    source_snapshot_ref TEXT NOT NULL DEFAULT '',
    operation_kind      TEXT NOT NULL DEFAULT 'update',
    predecessor_version TEXT NOT NULL DEFAULT '',
    promoted            BOOLEAN NOT NULL DEFAULT 0
);

CREATE INDEX idx_dictionary_history_created_at ON dictionary_history (created_at);
