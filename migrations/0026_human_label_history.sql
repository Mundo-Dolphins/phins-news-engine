-- migrations/0026_human_label_history.sql
--
-- Nothing stops a second, contradictory human_labels /
-- editorial_examples row for the same article_id.
--
-- Fix: both tables grow a revision/active/supersedes/content_hash/
-- source/session_id set of columns. Going forward, application code
-- (adapters/outbound/sqlite/human_label_repository.go's
-- Label/Relabel/Retract) only ever INSERTs a new row and flips the
-- prior active row's `active` to 0 -- never UPDATEs a row's label in
-- place, never DELETEs. This migration only backfills existing rows
-- into that shape.
--
-- Backfill: for each table, rows are grouped by article_id and ranked
-- by (labeled_at ASC, id ASC). A group is "ambiguous" only when two of
-- its rows share the exact same labeled_at AND disagree on label --
-- there is no way to order those without guessing, so per the design
-- spec they are NOT auto-resolved: every row in an ambiguous group
-- gets active = 0, and one summary row is written to
-- human_label_conflicts. Every other group (agreeing labels, or
-- disagreeing labels with an unambiguous timestamp order) is folded
-- into a revision chain: earliest row is revision 1, each subsequent
-- row's supersedes points at the previous row's revision, and the last
-- (highest rn) row is active = 1. Nothing is ever deleted -- an
-- ambiguous group's rows still get sequential revision numbers, they
-- are just all inactive until `label resolve-conflict` adds a new,
-- explicitly-authorized active revision on top.
--
-- Verification: after backfill, this migration asserts (a) no
-- article_id has more than one active=1 row in either table, and (b)
-- human_label_conflicts' per-table row count matches the number of
-- ambiguous groups detected. Both are expressed as INSERTs into a TEMP
-- TABLE with CHECK(ok = 0) (see migration 0020 for the same pattern):
-- a nonzero computed value fails the INSERT, which fails this file's
-- Exec call, which rolls back the whole migration transaction.
--
-- Operational ordering during an upgrade: `editorial cycle`'s sync step
-- carries locally-owned tables forward with a *positional*
-- `INSERT INTO main.<t> SELECT * FROM previous.<t>`
-- (infra/sqlite/merge.go's MergeLocalOwnedTables),
-- and that copy runs BEFORE Migrate(). A version-skewed sync -- local DB
-- already at 0026 while the promoted snapshot is pre-0026, or the
-- reverse -- therefore fails with a column-count mismatch on
-- human_labels/editorial_examples. This is a pre-existing property of
-- the merge mechanism, not a new bug, but the six columns added below
-- re-arm it: upgrade both sides across this migration and run a full
-- `editorial cycle` sync afterwards, rather than straddling the
-- boundary. See docs/RELEASE-CHECKLIST.md, "Deploy procedure".
--
-- Rollback (operational -- this codebase has no down-migrations): this
-- migration only adds columns/tables/indexes and never removes or
-- rewrites a human_labels/editorial_examples row's original label/
-- labeled_at/labeled_by/reason/notes values. To revert: `DROP INDEX
-- idx_human_labels_one_active; DROP INDEX
-- idx_editorial_examples_one_active; DROP TABLE
-- human_label_conflicts;` then drop the six new columns from each
-- table (SQLite >= 3.35 supports `ALTER TABLE ... DROP COLUMN`), and
-- delete this migration's row from schema_migrations to let it reapply
-- later.

ALTER TABLE human_labels ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE human_labels ADD COLUMN active INTEGER NOT NULL DEFAULT 0;
ALTER TABLE human_labels ADD COLUMN supersedes INTEGER;
ALTER TABLE human_labels ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE human_labels ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE human_labels ADD COLUMN session_id TEXT;

ALTER TABLE editorial_examples ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE editorial_examples ADD COLUMN active INTEGER NOT NULL DEFAULT 0;
ALTER TABLE editorial_examples ADD COLUMN supersedes INTEGER;
ALTER TABLE editorial_examples ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE editorial_examples ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE editorial_examples ADD COLUMN session_id TEXT;

CREATE TABLE human_label_conflicts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    table_name   TEXT NOT NULL,
    article_id   TEXT NOT NULL,
    detail       TEXT NOT NULL,
    detected_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at  TIMESTAMP
);
CREATE INDEX idx_human_label_conflicts_unresolved ON human_label_conflicts (article_id) WHERE resolved_at IS NULL;

-- ===== human_labels backfill =====

CREATE TEMP TABLE t26_hl_ambiguous AS
SELECT DISTINCT a.article_id
FROM human_labels a
JOIN human_labels b ON a.article_id = b.article_id AND a.id <> b.id
WHERE a.labeled_at = b.labeled_at AND a.label <> b.label;

CREATE TEMP TABLE t26_hl_ranked AS
SELECT id, article_id,
       ROW_NUMBER() OVER (PARTITION BY article_id ORDER BY labeled_at ASC, id ASC) AS rn,
       COUNT(*) OVER (PARTITION BY article_id) AS cnt
FROM human_labels;

UPDATE human_labels
SET revision   = (SELECT rn FROM t26_hl_ranked r WHERE r.id = human_labels.id),
    supersedes = (SELECT CASE WHEN rn > 1 THEN rn - 1 ELSE NULL END FROM t26_hl_ranked r WHERE r.id = human_labels.id),
    active     = (SELECT CASE
                      WHEN human_labels.article_id IN (SELECT article_id FROM t26_hl_ambiguous) THEN 0
                      WHEN rn = cnt THEN 1
                      ELSE 0
                  END FROM t26_hl_ranked r WHERE r.id = human_labels.id);

INSERT INTO human_label_conflicts (table_name, article_id, detail)
SELECT 'human_labels', h.article_id,
       GROUP_CONCAT(h.id || ':' || h.label || ':' || h.labeled_at, ' | ')
FROM human_labels h
WHERE h.article_id IN (SELECT article_id FROM t26_hl_ambiguous)
GROUP BY h.article_id;

-- ===== editorial_examples backfill (independent grouping -- see the
-- migration's module comment: this table has no FK to articles, so it
-- can carry history for an article_id whose human_labels rows were
-- already cascade-deleted by a past retention purge) =====

CREATE TEMP TABLE t26_ee_ambiguous AS
SELECT DISTINCT a.article_id
FROM editorial_examples a
JOIN editorial_examples b ON a.article_id = b.article_id AND a.id <> b.id
WHERE a.labeled_at = b.labeled_at AND a.label <> b.label;

CREATE TEMP TABLE t26_ee_ranked AS
SELECT id, article_id,
       ROW_NUMBER() OVER (PARTITION BY article_id ORDER BY labeled_at ASC, id ASC) AS rn,
       COUNT(*) OVER (PARTITION BY article_id) AS cnt
FROM editorial_examples;

UPDATE editorial_examples
SET revision   = (SELECT rn FROM t26_ee_ranked r WHERE r.id = editorial_examples.id),
    supersedes = (SELECT CASE WHEN rn > 1 THEN rn - 1 ELSE NULL END FROM t26_ee_ranked r WHERE r.id = editorial_examples.id),
    active     = (SELECT CASE
                      WHEN editorial_examples.article_id IN (SELECT article_id FROM t26_ee_ambiguous) THEN 0
                      WHEN rn = cnt THEN 1
                      ELSE 0
                  END FROM t26_ee_ranked r WHERE r.id = editorial_examples.id);

INSERT INTO human_label_conflicts (table_name, article_id, detail)
SELECT 'editorial_examples', e.article_id,
       GROUP_CONCAT(e.id || ':' || e.label || ':' || e.labeled_at, ' | ')
FROM editorial_examples e
WHERE e.article_id IN (SELECT article_id FROM t26_ee_ambiguous)
GROUP BY e.article_id;

-- ===== verification =====

CREATE TEMP TABLE migration_0026_assert (ok INTEGER NOT NULL CHECK (ok = 0));

INSERT INTO migration_0026_assert (ok)
SELECT COUNT(*) FROM (
    SELECT article_id FROM human_labels WHERE active = 1 GROUP BY article_id HAVING COUNT(*) > 1
);
INSERT INTO migration_0026_assert (ok)
SELECT COUNT(*) FROM (
    SELECT article_id FROM editorial_examples WHERE active = 1 GROUP BY article_id HAVING COUNT(*) > 1
);
INSERT INTO migration_0026_assert (ok)
SELECT ABS(
    (SELECT COUNT(*) FROM human_label_conflicts WHERE table_name = 'human_labels') -
    (SELECT COUNT(*) FROM t26_hl_ambiguous)
);
INSERT INTO migration_0026_assert (ok)
SELECT ABS(
    (SELECT COUNT(*) FROM human_label_conflicts WHERE table_name = 'editorial_examples') -
    (SELECT COUNT(*) FROM t26_ee_ambiguous)
);

DROP TABLE migration_0026_assert;
DROP TABLE t26_hl_ambiguous;
DROP TABLE t26_hl_ranked;
DROP TABLE t26_ee_ambiguous;
DROP TABLE t26_ee_ranked;

-- ===== the real constraint =====

CREATE UNIQUE INDEX idx_human_labels_one_active ON human_labels (article_id) WHERE active = 1;
CREATE UNIQUE INDEX idx_editorial_examples_one_active ON editorial_examples (article_id) WHERE active = 1;
