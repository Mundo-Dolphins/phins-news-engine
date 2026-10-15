-- migrations/0016_source_policy_decisions.sql
--
-- source_policy_decisions is the audit trail for every Gate A/Gate B
-- evaluation Phase 12's source-policy engine performs (see
-- domain/sourcepolicy and app/usecase/
-- process_cycle.go). One row per evaluation where a non-default Policy
-- applied, or where the mode is shadow/enforce and the standard and
-- special outcomes diverge — this is what `source-policy audit` queries.
-- This includes Gate A's shadow-mode "would retain for clustering" preview
-- (decision_code = 'would_retain_near_duplicate'): those rows record only
-- that enforce mode would have let a near-duplicate through to clustering,
-- NOT the eventual publish outcome — clustering has not run yet at Gate A,
-- so would_publish_special is always '0' on those rows even though the
-- article would, in fact, have proceeded further under enforce mode. Do
-- not read would_publish_special='0' on a would_retain_near_duplicate row
-- as "this would never have published" — it means "unknown, not
-- evaluated."
-- would_publish_standard/would_publish_special are stored as '0'/'1' text,
-- matching this schema's existing boolean-as-text convention elsewhere
-- (see articles.is_trusted_source).
CREATE TABLE source_policy_decisions (
    id                      TEXT PRIMARY KEY,
    article_id              TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    source_id               TEXT NOT NULL,
    authority                TEXT NOT NULL,
    policy_version           TEXT NOT NULL,
    cluster_id               TEXT REFERENCES event_clusters(id) ON DELETE SET NULL,
    dedup_match_type         TEXT NOT NULL DEFAULT '',
    member_role              TEXT NOT NULL DEFAULT '',
    novelty                  TEXT NOT NULL DEFAULT '',
    decision_code            TEXT NOT NULL,
    would_publish_standard   TEXT NOT NULL DEFAULT '0',
    would_publish_special    TEXT NOT NULL DEFAULT '0',
    mode                     TEXT NOT NULL,
    explanation               TEXT NOT NULL DEFAULT '',
    decided_at                TIMESTAMP NOT NULL
);
CREATE INDEX idx_source_policy_decisions_article ON source_policy_decisions (article_id);
CREATE INDEX idx_source_policy_decisions_source ON source_policy_decisions (source_id, decided_at);
CREATE INDEX idx_source_policy_decisions_cluster ON source_policy_decisions (cluster_id, decided_at);
