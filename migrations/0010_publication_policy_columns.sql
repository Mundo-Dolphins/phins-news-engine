ALTER TABLE articles ADD COLUMN primary_entity TEXT NOT NULL DEFAULT '';
ALTER TABLE articles ADD COLUMN cluster_member_role TEXT NOT NULL DEFAULT '';
ALTER TABLE articles ADD COLUMN is_trusted_source INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_articles_status_published_at ON articles (status, published_at);
CREATE INDEX idx_articles_status_entity_published_at ON articles (status, primary_entity, published_at);
