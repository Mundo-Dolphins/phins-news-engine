-- migrations/0021_source_id_namespace.sql
--
-- Namespaces every source_id with its platform prefix ("platform:
-- identifier", e.g. "twitter:sprisotopes"), fixing the collision where
-- Twitter and Instagram both produced the bare source_id "sprisotopes"
-- for the same handle.
--
-- Ordering: source_policy_decisions and event_cluster_publications derive
-- platform via a join to articles (source_type/source_id) — they are
-- migrated FIRST, while articles.source_id is still in its pre-migration
-- bare form. articles is migrated after those two, once nothing else
-- depends on its old value. editorial_examples (own source_type column)
-- and seen_items (own platform column) are self-contained and independent
-- of this ordering.
--
-- Rollback (operational -- this codebase has no down-migrations, see
-- migrations/0020_editorial_examples.sql's convention): re-derive the old
-- value per row from source_id_migrations (new_source_id -> old_source_id)
-- and re-run the equivalent UPDATE in reverse, scoped to rows whose
-- current source_id matches new_source_id. Do this only if the deployed
-- binary is also being rolled back to a pre-#159 version — see the design
-- spec §6.

CREATE TABLE source_id_migrations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    table_name      TEXT NOT NULL,
    old_source_id   TEXT NOT NULL,
    new_source_id   TEXT NOT NULL,
    row_count       INTEGER NOT NULL,
    migrated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- source_policy_decisions: derive platform via article_id join, before
-- articles.source_id changes. The identifier half of the new value is this
-- row's OWN source_id (spd.source_id), not the joined article's current
-- source_id -- the two happen to be equal for every row this codebase
-- writes today, but only the row's own value is actually correct if they
-- were ever to diverge (e.g. a future rename-remap tool that edits an
-- existing article's source_id after the fact). Only the platform prefix
-- comes from the article join, since source_policy_decisions has no
-- source_type column of its own.
INSERT INTO source_id_migrations (table_name, old_source_id, new_source_id, row_count)
SELECT 'source_policy_decisions', spd.source_id, a.source_type || ':' || spd.source_id, COUNT(*)
FROM source_policy_decisions spd
JOIN articles a ON a.id = spd.article_id
GROUP BY spd.source_id, a.source_type;

UPDATE source_policy_decisions
SET source_id = (
    SELECT a.source_type || ':' || source_policy_decisions.source_id
    FROM articles a
    WHERE a.id = source_policy_decisions.article_id
)
WHERE EXISTS (SELECT 1 FROM articles a WHERE a.id = source_policy_decisions.article_id);

-- event_cluster_publications: same pattern -- identifier half from this
-- row's own source_id, platform prefix from the joined article.
INSERT INTO source_id_migrations (table_name, old_source_id, new_source_id, row_count)
SELECT 'event_cluster_publications', ecp.source_id, a.source_type || ':' || ecp.source_id, COUNT(*)
FROM event_cluster_publications ecp
JOIN articles a ON a.id = ecp.article_id
GROUP BY ecp.source_id, a.source_type;

UPDATE event_cluster_publications
SET source_id = (
    SELECT a.source_type || ':' || event_cluster_publications.source_id
    FROM articles a
    WHERE a.id = event_cluster_publications.article_id
)
WHERE EXISTS (SELECT 1 FROM articles a WHERE a.id = event_cluster_publications.article_id);

-- articles: now safe to rewrite in place — nothing above still needs the
-- old value.
INSERT INTO source_id_migrations (table_name, old_source_id, new_source_id, row_count)
SELECT 'articles', source_id, source_type || ':' || source_id, COUNT(*)
FROM articles
GROUP BY source_id, source_type;

UPDATE articles SET source_id = source_type || ':' || source_id;

-- editorial_examples: self-contained (own source_type column).
INSERT INTO source_id_migrations (table_name, old_source_id, new_source_id, row_count)
SELECT 'editorial_examples', source_id, source_type || ':' || source_id, COUNT(*)
FROM editorial_examples
GROUP BY source_id, source_type;

UPDATE editorial_examples SET source_id = source_type || ':' || source_id;

-- seen_items: self-contained (own platform column).
INSERT INTO source_id_migrations (table_name, old_source_id, new_source_id, row_count)
SELECT 'seen_items', source_id, platform || ':' || source_id, COUNT(*)
FROM seen_items
GROUP BY source_id, platform;

UPDATE seen_items SET source_id = platform || ':' || source_id;

-- Verification: abort the whole transaction (see sqlite.applyMigration's
-- BeginTx/defer Rollback/Commit) if any row ended up without a prefix that
-- matches its own known platform, or if any table's row count changed
-- (namespacing must never add/drop rows) -- matching the CHECK(ok = 0)
-- pattern established in migrations/0020_editorial_examples.sql.
--
-- Platform check: rather than a fixed literal whitelist (which would drift
-- out of sync with domain/source.Type -- e.g. it would have to
-- separately hardcode source.TypeOther = "other" and every future platform
-- added there), each row's prefix (before the first ':') is compared
-- against that same row's own known-platform column: source_type for
-- articles/editorial_examples, the joined articles.source_type (via
-- article_id) for source_policy_decisions/event_cluster_publications, and
-- platform for seen_items. This can never drift from the domain model,
-- since it *is* the domain model's own value for that row. The
-- INSTR(source_id, ':') <= 1 check is kept alongside it to still catch a
-- missing/empty prefix outright (including the blank-source_type case a
-- handful of this file's own migration tests exercise).
CREATE TEMP TABLE migration_0021_assert (ok INTEGER NOT NULL CHECK (ok = 0));

INSERT INTO migration_0021_assert (ok)
SELECT COUNT(*) FROM articles
WHERE INSTR(source_id, ':') <= 1
   OR SUBSTR(source_id, 1, INSTR(source_id, ':') - 1) != source_type;

INSERT INTO migration_0021_assert (ok)
SELECT COUNT(*) FROM editorial_examples
WHERE INSTR(source_id, ':') <= 1
   OR SUBSTR(source_id, 1, INSTR(source_id, ':') - 1) != source_type;

INSERT INTO migration_0021_assert (ok)
SELECT COUNT(*) FROM source_policy_decisions spd
WHERE INSTR(spd.source_id, ':') <= 1
   OR SUBSTR(spd.source_id, 1, INSTR(spd.source_id, ':') - 1) != (
       SELECT a.source_type FROM articles a WHERE a.id = spd.article_id
   );

INSERT INTO migration_0021_assert (ok)
SELECT COUNT(*) FROM event_cluster_publications ecp
WHERE INSTR(ecp.source_id, ':') <= 1
   OR SUBSTR(ecp.source_id, 1, INSTR(ecp.source_id, ':') - 1) != (
       SELECT a.source_type FROM articles a WHERE a.id = ecp.article_id
   );

INSERT INTO migration_0021_assert (ok)
SELECT COUNT(*) FROM seen_items
WHERE INSTR(source_id, ':') <= 1
   OR SUBSTR(source_id, 1, INSTR(source_id, ':') - 1) != platform;

INSERT INTO migration_0021_assert (ok)
SELECT ABS((SELECT COUNT(*) FROM articles) - (SELECT COALESCE(SUM(row_count), 0) FROM source_id_migrations WHERE table_name = 'articles'));

INSERT INTO migration_0021_assert (ok)
SELECT ABS((SELECT COUNT(*) FROM editorial_examples) - (SELECT COALESCE(SUM(row_count), 0) FROM source_id_migrations WHERE table_name = 'editorial_examples'));

DROP TABLE migration_0021_assert;
