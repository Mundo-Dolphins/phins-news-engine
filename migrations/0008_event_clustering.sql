-- event_cluster_members gains a role (see clustering.MemberRole) and its
-- own clustering_score, distinct from the pre-existing similarity_score
-- column: similarity_score is reserved for a future text-similarity/dedup
-- metric (it is never written by this migration's callers), while
-- clustering_score is the winning clustering.Comparison.Score that decided
-- this member's join — a different scale (weighted signal average, not
-- shingle/text overlap) and a different producer (domain/
-- clustering, not domain/deduplication). Per-member evidence is
-- deliberately NOT duplicated onto this table: event_clustering_decisions
-- below already records one full audit row (action, member_role, score,
-- alternatives_json, evidence_json, rules_version, decided_at) per
-- article_id, and every clustered member's article_id has exactly one such
-- row, so querying event_clustering_decisions by article_id already gives
-- complete join evidence without a second copy to keep in sync.
ALTER TABLE event_cluster_members ADD COLUMN role TEXT NOT NULL DEFAULT 'first_report';
ALTER TABLE event_cluster_members ADD COLUMN clustering_score REAL;

-- event_representations stores one row per article: the eventsignals.Extract
-- output, both as full-fidelity JSON (representation_json, used to
-- reconstitute eventsignals.Representation wholesale for candidate scoring)
-- and as a handful of columns pulled out for cheap inspection/debugging
-- without a JSON decode. rules_version is NOT a field on
-- eventsignals.Representation itself (only eventsignals.Rules.Version is),
-- so callers pass it in alongside the Representation being saved.
-- event_date_window stores Representation.EventWindow (nanoseconds, via
-- time.Duration) rather than EventDate: EventDate duplicates
-- articles.published_at and representation_json, while EventWindow is the
-- one piece of per-representation information not already queryable
-- elsewhere, in case a later task wants to filter/inspect it directly.
CREATE TABLE event_representations (
    article_id           TEXT PRIMARY KEY REFERENCES articles(id) ON DELETE CASCADE,
    rules_version         TEXT NOT NULL DEFAULT '',
    event_type            TEXT NOT NULL DEFAULT '',
    primary_entity        TEXT NOT NULL DEFAULT '',
    primary_action        TEXT NOT NULL DEFAULT '',
    event_date_window     INTEGER NOT NULL DEFAULT 0,
    representation_json   TEXT NOT NULL,
    created_at            TIMESTAMP NOT NULL
);

-- event_keys is what makes candidate lookup bounded: a new incoming
-- article's Representation.ApproximateKeys are looked up against this
-- table's event_key index, never against a full article/representation
-- scan. strength is the key's ordinal position within ApproximateKeys (0 =
-- ApproximateKeys[0], the strongest/most-specific key; increasing values
-- are progressively weaker/broader) so a lookup can ORDER BY strength ASC
-- to prefer strong-key matches over weak ones.
CREATE TABLE event_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_key   TEXT NOT NULL,
    article_id  TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    cluster_id  TEXT NOT NULL REFERENCES event_clusters(id) ON DELETE CASCADE,
    strength    INTEGER NOT NULL
);
CREATE INDEX idx_event_keys_event_key ON event_keys (event_key);
CREATE INDEX idx_event_keys_cluster_id ON event_keys (cluster_id);
CREATE UNIQUE INDEX idx_event_keys_article_key ON event_keys (article_id, event_key);

-- event_clustering_decisions is the full audit trail for one
-- clustering.Decision per article: what was chosen, the winning score and
-- evidence, and every alternative cluster considered (Decision has no
-- ArticleID field of its own, so chosen_cluster_id/article_id are supplied
-- by the caller alongside the Decision being saved; chosen_cluster_id is
-- the cluster the article actually ended up in, which for ActionCreate is
-- the newly created cluster's id even though Decision.ClusterID itself is
-- only ever set by the domain package for ActionJoin).
CREATE TABLE event_clustering_decisions (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id         TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    chosen_cluster_id  TEXT REFERENCES event_clusters(id) ON DELETE SET NULL,
    action             TEXT NOT NULL,
    member_role        TEXT NOT NULL DEFAULT '',
    score              REAL NOT NULL DEFAULT 0,
    alternatives_json  TEXT NOT NULL DEFAULT '[]',
    evidence_json      TEXT NOT NULL DEFAULT '[]',
    rules_version      TEXT NOT NULL DEFAULT '',
    decided_at         TIMESTAMP NOT NULL
);
CREATE INDEX idx_event_clustering_decisions_article ON event_clustering_decisions (article_id);

-- event_clusters gains columns for the CURRENT representative's score/
-- evidence/rules version, for cheap reads without a join. A representative
-- switch is also required to be auditable rather than silently overwriting
-- the prior value (the plan's "explainable" constraint), so
-- event_cluster_representative_history is a separate append-only table —
-- the same audit-log shape as event_clustering_decisions and
-- article_processing_events elsewhere in this schema — recording every
-- switch's outgoing score/evidence/rules_version and when it happened. The
-- very first representative (set by CreateCluster for a cluster's founding
-- article) intentionally does NOT get a history row: there is nothing to
-- score a single-member cluster's representative against yet, so
-- event_clusters.first_seen_at/representative_article_id already fully
-- describes that initial, unscored assignment.
ALTER TABLE event_clusters ADD COLUMN representative_score REAL;
ALTER TABLE event_clusters ADD COLUMN representative_evidence_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE event_clusters ADD COLUMN representative_rules_version TEXT NOT NULL DEFAULT '';

CREATE TABLE event_cluster_representative_history (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    cluster_id     TEXT NOT NULL REFERENCES event_clusters(id) ON DELETE CASCADE,
    article_id     TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    score          REAL NOT NULL,
    evidence_json  TEXT NOT NULL DEFAULT '[]',
    rules_version  TEXT NOT NULL DEFAULT '',
    switched_at    TIMESTAMP NOT NULL
);
CREATE INDEX idx_event_cluster_representative_history_cluster ON event_cluster_representative_history (cluster_id);
