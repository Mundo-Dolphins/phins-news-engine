CREATE TABLE event_clusters (
    id                         TEXT PRIMARY KEY,
    representative_article_id TEXT REFERENCES articles(id) ON DELETE SET NULL,
    title                      TEXT NOT NULL DEFAULT '',
    summary                    TEXT NOT NULL DEFAULT '',
    first_seen_at              TIMESTAMP NOT NULL,
    last_updated_at            TIMESTAMP NOT NULL,
    member_count               INTEGER NOT NULL DEFAULT 0,
    status                     TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed'))
);
CREATE INDEX idx_event_clusters_status ON event_clusters (status);
CREATE INDEX idx_event_clusters_last_updated ON event_clusters (last_updated_at);

CREATE TABLE event_cluster_members (
    cluster_id       TEXT NOT NULL REFERENCES event_clusters(id) ON DELETE CASCADE,
    article_id       TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    joined_at        TIMESTAMP NOT NULL,
    similarity_score REAL,
    PRIMARY KEY (cluster_id, article_id)
);

ALTER TABLE articles ADD COLUMN current_cluster_id TEXT REFERENCES event_clusters(id) ON DELETE SET NULL;
