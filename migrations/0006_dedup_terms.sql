CREATE TABLE article_dedup_terms (
    article_id  TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    term        TEXT NOT NULL,
    PRIMARY KEY (article_id, term)
);
CREATE INDEX idx_dedup_terms_term ON article_dedup_terms (term);
