-- migrations/0020_editorial_examples.sql
--
-- Bug: human_labels.article_id is REFERENCES articles(id) ON DELETE
-- CASCADE (migration 0005). RunRetention purges articles whose status is
-- terminal (rejected_*, discarded) once they're past their configured
-- retention window -- app/usecase/run_retention.go. The
-- cascade means every human label on a purged article disappears with
-- it, silently: editorialtrain.ExportDataset (app/
-- editorialtrain/dataset.go) discovers the article is gone via
-- ArticleRepository.FindByID and just counts it as "skipped", never
-- failing the export.
--
-- Fix: a new table, editorial_examples, durably snapshots every field
-- domain/editorial.ExtractFeatures needs (title, body, source_id,
-- source_type, domain, content_type, published_at, fetched_at) plus
-- provenance (author, external_id, original_url, canonical_url,
-- classifier_version, dictionary_version, cluster_id) at the moment a
-- human label is saved. It deliberately has NO foreign key to articles
-- (or event_clusters): article_id and cluster_id are plain, unenforced
-- text columns, so a future DELETE FROM articles can never cascade into
-- this table, structurally, no matter what deletes the article. Going
-- forward, adapters/outbound/sqlite/human_label_repository.go's
-- Save/DeleteByArticle write/clear this table transactionally alongside
-- human_labels (see Task 2) -- this migration only creates the table and
-- backfills what already exists.
--
-- Backfill safety: human_labels.article_id currently has ON DELETE
-- CASCADE, which means it is theoretically impossible for an existing
-- human_labels row to reference a since-deleted articles row -- if the
-- article was ever deleted, the cascade would already have deleted the
-- label row too. So the JOIN below is not a best-effort backfill; every
-- human_labels row that exists right now is guaranteed to have a live
-- articles row to join against. (Labels lost by a *past* retention run,
-- before this fix, are unfortunately gone already -- there is nothing
-- left anywhere to recover them from. This migration only stops future
-- loss.)
--
-- Verification: after the backfill, this migration asserts (a) the row
-- count of editorial_examples equals the row count of human_labels, and
-- (b) every human_labels row has a matching (article_id, label,
-- labeled_at) triple in editorial_examples. Both checks are expressed as
-- an INSERT into a TEMP TABLE with a CHECK(ok = 0) constraint: if the
-- computed value is nonzero, the INSERT itself violates the CHECK and
-- fails, which fails this file's single multi-statement Exec call, which
-- rolls back the whole migration transaction (see migrate.go's
-- applyMigration: BeginTx/defer Rollback/Commit) -- the migration stops
-- cold on any inconsistency, and since this file never modifies
-- human_labels or articles, they are left completely untouched either
-- way.
--
-- Rollback (operational -- this codebase has no down-migrations): this
-- migration is purely additive. To revert: `DROP TABLE
-- editorial_examples;` and delete its row from schema_migrations if you
-- want Migrate to reapply it later. Nothing else needs to change.

CREATE TABLE editorial_examples (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    article_id          TEXT NOT NULL,
    label               TEXT NOT NULL,
    reason              TEXT NOT NULL DEFAULT '',
    labeled_by          TEXT NOT NULL DEFAULT '',
    labeled_at          TIMESTAMP NOT NULL,
    notes               TEXT NOT NULL DEFAULT '',
    title               TEXT NOT NULL DEFAULT '',
    body                TEXT NOT NULL DEFAULT '',
    source_id           TEXT NOT NULL DEFAULT '',
    source_type         TEXT NOT NULL DEFAULT '',
    domain              TEXT NOT NULL DEFAULT '',
    content_type        TEXT NOT NULL DEFAULT '',
    published_at        TIMESTAMP,
    fetched_at          TIMESTAMP NOT NULL,
    cluster_id          TEXT NOT NULL DEFAULT '',
    author              TEXT NOT NULL DEFAULT '',
    external_id         TEXT NOT NULL DEFAULT '',
    original_url        TEXT NOT NULL DEFAULT '',
    canonical_url       TEXT NOT NULL DEFAULT '',
    classifier_version  TEXT NOT NULL DEFAULT '',
    dictionary_version  TEXT NOT NULL DEFAULT '',
    captured_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_editorial_examples_article ON editorial_examples (article_id);
CREATE INDEX idx_editorial_examples_labeled_at ON editorial_examples (labeled_at);

INSERT INTO editorial_examples (
    article_id, label, reason, labeled_by, labeled_at, notes,
    title, body, source_id, source_type, domain, content_type,
    published_at, fetched_at, cluster_id, author, external_id, original_url, canonical_url,
    classifier_version, dictionary_version
)
SELECT
    hl.article_id, hl.label, hl.reason, hl.labeled_by, hl.labeled_at, hl.notes,
    a.title, a.summary, a.source_id, a.source_type, a.domain, a.content_type,
    a.published_at, a.fetched_at, COALESCE(a.current_cluster_id, ''), a.author, a.external_id, a.original_url, a.canonical_url,
    COALESCE(a.classifier_version, ''), COALESCE(a.dictionary_version, '')
FROM human_labels hl
JOIN articles a ON a.id = hl.article_id
ORDER BY hl.id;

CREATE TEMP TABLE migration_0020_assert (ok INTEGER NOT NULL CHECK (ok = 0));

-- Check (a): same number of rows.
INSERT INTO migration_0020_assert (ok)
SELECT ABS(
    (SELECT COUNT(*) FROM human_labels) - (SELECT COUNT(*) FROM editorial_examples)
);

-- Check (b): every human_labels row has a matching editorial_examples row.
INSERT INTO migration_0020_assert (ok)
SELECT COUNT(*) FROM human_labels hl
WHERE NOT EXISTS (
    SELECT 1 FROM editorial_examples ee
    WHERE ee.article_id = hl.article_id AND ee.label = hl.label AND ee.labeled_at = hl.labeled_at
);

DROP TABLE migration_0020_assert;
