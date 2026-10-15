-- migrations/0018_cluster_publication_gate.sql
--
-- event_cluster_publications is the persistence-level guarantee that a
-- cluster (event_clusters row) produces at most one *initial* publication,
-- independent of sourcepolicy.Mode. One row
-- per cluster_id (PRIMARY KEY), acquired via a compare-and-swap UPSERT
-- (ports/out.ClusterPublicationRepository.Reserve):
--
--   INSERT INTO event_cluster_publications (...) VALUES (...)
--   ON CONFLICT(cluster_id) DO UPDATE SET ... WHERE state = 'released'
--
-- which either inserts (no row existed), updates (existing row was
-- 'released'), or silently changes nothing and reports zero rows affected
-- (existing row is 'reserved' or 'confirmed') — no error is raised on a
-- lost compare-and-swap, this is standard SQLite upsert behavior. State
-- transitions: 'reserved' (accept() enqueued the outbox item, in the same
-- transaction) -> 'confirmed' (PublishOutbox delivered it) or 'released'
-- (PublishOutbox exhausted retries/hit a permanent error; a later article
-- may then re-reserve). article_id/source_id/member_role/decision_code/
-- policy_version/reason are overwritten on every successful Reserve
-- (including a re-reservation after release) so the row always reflects
-- the CURRENT holder — this is also what scopes Confirm/Release to a
-- no-op for any article that never actually held the reservation (e.g. a
-- later material-update publication for the same cluster).
CREATE TABLE event_cluster_publications (
    cluster_id      TEXT PRIMARY KEY REFERENCES event_clusters(id) ON DELETE CASCADE,
    state           TEXT NOT NULL CHECK (state IN ('reserved','confirmed','released')),
    article_id      TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    source_id       TEXT NOT NULL DEFAULT '',
    member_role     TEXT NOT NULL DEFAULT '',
    decision_code   TEXT NOT NULL DEFAULT '',
    policy_version  TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT '',
    outbox_id       INTEGER REFERENCES publication_outbox(id) ON DELETE SET NULL,
    reserved_at     TIMESTAMP NOT NULL,
    confirmed_at    TIMESTAMP,
    released_at     TIMESTAMP
);
CREATE INDEX idx_event_cluster_publications_article ON event_cluster_publications (article_id);

-- Backfill so an existing deployment starts consistent: every cluster that
-- already has a successfully published article gets a 'confirmed' row for
-- the EARLIEST such article (by publication_attempts.attempted_at,
-- partitioned by cluster). Pre-existing duplicate publications — the bug
-- this migration's application-code counterpart fixes — are deliberately
-- NOT modified or unpublished; only the earliest is backfilled as the
-- gate's confirmed holder. Clusters whose only relevant activity is a
-- still in-flight outbox item (status 'pending' or 'claimed' — this
-- covers pending_retry articles too, since a temporary failure returns
-- the outbox row to 'pending') get a 'reserved' row instead, for the
-- earliest such article, and only when no 'confirmed' row was already
-- backfilled for that cluster by the block above. A cluster with neither
-- (nothing ever published or in flight) gets no row — exactly like a
-- brand-new cluster post-deploy; the first real article to reach
-- ProcessCycle's gate check for it will legitimately create one.
INSERT INTO event_cluster_publications (
    cluster_id, state, article_id, source_id, member_role, decision_code, policy_version, reason,
    outbox_id, reserved_at, confirmed_at
)
SELECT
    ranked.cluster_id, 'confirmed', ranked.article_id, ranked.source_id, ranked.cluster_member_role, '', '',
    'backfilled by migration 0018 from the earliest published article in this cluster',
    ranked.outbox_id, ranked.attempted_at, ranked.attempted_at
FROM (
    SELECT
        a.current_cluster_id AS cluster_id, a.id AS article_id, a.source_id AS source_id,
        a.cluster_member_role AS cluster_member_role, po.id AS outbox_id, pa.attempted_at AS attempted_at,
        ROW_NUMBER() OVER (PARTITION BY a.current_cluster_id ORDER BY pa.attempted_at ASC) AS rn
    FROM articles a
    JOIN publication_outbox po ON po.article_id = a.id
    JOIN publication_attempts pa ON pa.outbox_id = po.id AND pa.outcome = 'succeeded'
    WHERE a.current_cluster_id IS NOT NULL AND po.status = 'published'
) ranked
WHERE ranked.rn = 1;

INSERT INTO event_cluster_publications (
    cluster_id, state, article_id, source_id, member_role, decision_code, policy_version, reason,
    outbox_id, reserved_at
)
SELECT
    ranked.cluster_id, 'reserved', ranked.article_id, ranked.source_id, ranked.cluster_member_role, '', '',
    'backfilled by migration 0018 from an in-flight (not yet published) outbox item in this cluster',
    ranked.outbox_id, ranked.created_at
FROM (
    SELECT
        a.current_cluster_id AS cluster_id, a.id AS article_id, a.source_id AS source_id,
        a.cluster_member_role AS cluster_member_role, po.id AS outbox_id, po.created_at AS created_at,
        ROW_NUMBER() OVER (PARTITION BY a.current_cluster_id ORDER BY po.created_at ASC) AS rn
    FROM articles a
    JOIN publication_outbox po ON po.article_id = a.id
    WHERE a.current_cluster_id IS NOT NULL AND po.status IN ('pending', 'claimed')
      AND a.current_cluster_id NOT IN (SELECT cluster_id FROM event_cluster_publications)
) ranked
WHERE ranked.rn = 1;
