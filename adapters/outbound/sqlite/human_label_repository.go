package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// HumanLabelRepository implements ports/out.HumanLabelRepository against
// SQLite.
type HumanLabelRepository struct {
	db *sql.DB
}

// NewHumanLabelRepository builds a HumanLabelRepository over an
// already-open, already-migrated database.
func NewHumanLabelRepository(db *sql.DB) *HumanLabelRepository {
	return &HumanLabelRepository{db: db}
}

var _ appout.HumanLabelRepository = (*HumanLabelRepository)(nil)

// Label implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) Label(ctx context.Context, e training.Example) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM human_labels WHERE article_id = ? AND active = 1`, e.ArticleID).Scan(&active); err != nil {
			return fmt.Errorf("sqlite: check active label for %s: %w", e.ArticleID, err)
		}
		if active > 0 {
			return fmt.Errorf("sqlite: label %s: %w", e.ArticleID, training.ErrAlreadyLabeled)
		}
		// Defensive resync: editorial_examples has no FK to articles and
		// survives retention purges that cascade-delete human_labels rows
		// (see migration 0026's module comment and 0020's design doc).
		// Migration 0026's backfill also ranks each table's revisions
		// independently per article_id. So it's possible -- if
		// articleID's human_labels rows were cascade-deleted by a past
		// retention purge and the article was later re-ingested -- for
		// editorial_examples to still hold a stale active=1 row at some
		// other revision even though human_labels (just checked above)
		// has none. Clear it unconditionally before inserting revision 1:
		// at this point in Label, any editorial_examples active row is
		// definitionally stale, since human_labels has already confirmed
		// there is no live label for this article. This is the same
		// "supersede, never delete" invariant as everywhere else in this
		// file, and it re-establishes the two tables' active-row revision
		// numbers in lockstep going forward. supersede below heals the
		// same divergence for the relabel/retract path, by clearing
		// whatever editorial_examples row is active rather than one at a
		// specific revision.
		if _, err := tx.ExecContext(ctx, `UPDATE editorial_examples SET active = 0 WHERE article_id = ? AND active = 1`, e.ArticleID); err != nil {
			return fmt.Errorf("sqlite: clear stale editorial_examples active row for %s: %w", e.ArticleID, err)
		}
		return r.insertRevision(ctx, tx, e, 1, 0)
	})
}

// Relabel implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) Relabel(ctx context.Context, e training.Example, expectedRevision int) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := r.supersede(ctx, tx, e.ArticleID, expectedRevision); err != nil {
			return err
		}
		return r.insertRevision(ctx, tx, e, expectedRevision+1, expectedRevision)
	})
}

// Retract implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) Retract(ctx context.Context, articleID string, expectedRevision int) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		return r.supersede(ctx, tx, articleID, expectedRevision)
	})
}

// supersede flips human_labels' active row at expectedRevision to
// inactive, plus whatever row (if any) is currently active for articleID
// in editorial_examples. Returns training.ErrNotLabeled wrapped if
// articleID has no active revision, or training.ErrRevisionConflict
// wrapped if its active revision is not expectedRevision. human_labels is
// the sole authority for both error cases; editorial_examples is
// resynced, never validated, for the reasons documented below.
func (r *HumanLabelRepository) supersede(ctx context.Context, tx *sql.Tx, articleID string, expectedRevision int) error {
	res, err := tx.ExecContext(ctx, `UPDATE human_labels SET active = 0 WHERE article_id = ? AND active = 1 AND revision = ?`, articleID, expectedRevision)
	if err != nil {
		return fmt.Errorf("sqlite: supersede human_labels for %s: %w", articleID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: supersede human_labels for %s: rows affected: %w", articleID, err)
	}
	if n == 0 {
		var currentRevision sql.NullInt64
		lookupErr := tx.QueryRowContext(ctx, `SELECT revision FROM human_labels WHERE article_id = ? AND active = 1`, articleID).Scan(&currentRevision)
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return fmt.Errorf("sqlite: read active revision for %s: %w", articleID, lookupErr)
		}
		if !currentRevision.Valid {
			return fmt.Errorf("sqlite: %s: %w", articleID, training.ErrNotLabeled)
		}
		return fmt.Errorf("sqlite: %s: expected active revision %d, found %d: %w", articleID, expectedRevision, currentRevision.Int64, training.ErrRevisionConflict)
	}
	// Deliberately not filtered by revision. idx_editorial_examples_one_active
	// guarantees at most one active=1 row per article_id, so "whatever is
	// active for this article" is unambiguous -- and it may legitimately
	// sit at a *different* revision number than human_labels': migration
	// 0026 ranks each table's revisions independently per article_id
	// (editorial_examples has no FK to articles and can carry history a
	// retention purge cascade-deleted from human_labels). Matching on the
	// revision too would leave such an article permanently un-relabelable.
	// Zero rows affected is likewise fine -- editorial_examples simply has
	// no active row for this article -- because the caller's next step
	// (insertRevision) writes the new active row at human_labels' new
	// revision number regardless, self-healing any divergence, exactly as
	// Label's defensive resync does for the first-label case.
	if _, err := tx.ExecContext(ctx, `UPDATE editorial_examples SET active = 0 WHERE article_id = ? AND active = 1`, articleID); err != nil {
		return fmt.Errorf("sqlite: supersede editorial_examples for %s: %w", articleID, err)
	}
	return nil
}

// insertRevision snapshots articles for e.ArticleID, computes its
// content hash, and inserts revision into both human_labels and
// editorial_examples, active=1, supersedes=supersedesRevision (0 means
// NULL -- no prior revision).
func (r *HumanLabelRepository) insertRevision(ctx context.Context, tx *sql.Tx, e training.Example, revision, supersedesRevision int) error {
	var title, body, sourceID, sourceType, domain, contentType, clusterID, author, externalID, originalURL, canonicalURL, classifierVersion, dictionaryVersion string
	var publishedAt sqliteNullTime
	var fetchedAt sqliteNullTime
	err := tx.QueryRowContext(ctx, `
		SELECT title, summary, source_id, source_type, domain, content_type,
			published_at, fetched_at, COALESCE(current_cluster_id, ''), author, external_id, original_url, canonical_url,
			COALESCE(classifier_version, ''), COALESCE(dictionary_version, '')
		FROM articles WHERE id = ?
	`, e.ArticleID).Scan(&title, &body, &sourceID, &sourceType, &domain, &contentType,
		&publishedAt, &fetchedAt, &clusterID, &author, &externalID, &originalURL, &canonicalURL,
		&classifierVersion, &dictionaryVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite: label %s: no such article", e.ArticleID)
	}
	if err != nil {
		return fmt.Errorf("sqlite: read article %s for label: %w", e.ArticleID, err)
	}

	sum := sha256.Sum256([]byte(title + "\x00" + body))
	contentHash := hex.EncodeToString(sum[:])
	supersedes := nullableSupersedes(supersedesRevision)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO human_labels (article_id, label, reason, labeled_by, labeled_at, notes, revision, active, supersedes, content_hash, source, session_id)
		VALUES (?,?,?,?,?,?,?,1,?,?,?,?)
	`, e.ArticleID, e.Label, e.Reason, e.LabeledBy, sqliteTime(e.LabeledAt), e.Notes, revision, supersedes, contentHash, e.Source, nullableString(e.SessionID)); err != nil {
		return fmt.Errorf("sqlite: insert human_labels revision for %s: %w", e.ArticleID, err)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO editorial_examples (
			article_id, label, reason, labeled_by, labeled_at, notes,
			title, body, source_id, source_type, domain, content_type,
			published_at, fetched_at, cluster_id, author, external_id, original_url, canonical_url,
			classifier_version, dictionary_version, revision, active, supersedes, content_hash, source, session_id
		)
		VALUES (?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?,?, ?,?,?,1,?,?,?,?)
	`, e.ArticleID, e.Label, e.Reason, e.LabeledBy, e.LabeledAt, e.Notes,
		title, body, sourceID, sourceType, domain, contentType,
		nullableTime(publishedAt.Time), nullableTime(fetchedAt.Time), clusterID, author, externalID, originalURL, canonicalURL,
		classifierVersion, dictionaryVersion, revision, supersedes, contentHash, e.Source, nullableString(e.SessionID))
	if err != nil {
		return fmt.Errorf("sqlite: insert editorial_examples revision for %s: %w", e.ArticleID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("sqlite: insert editorial_examples revision for %s: no rows inserted", e.ArticleID)
	}
	return nil
}

// nullableSupersedes maps 0 (the "no prior revision" sentinel used by
// Label) to SQL NULL, and any other value to itself.
func nullableSupersedes(revision int) any {
	if revision == 0 {
		return nil
	}
	return revision
}

// GetActive implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) GetActive(ctx context.Context, articleID string) (training.Example, bool, error) {
	var e training.Example
	var supersedes sql.NullInt64
	var sessionID sql.NullString
	err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT article_id, label, reason, labeled_by, labeled_at, notes, revision, supersedes, content_hash, source, session_id
		FROM human_labels WHERE article_id = ? AND active = 1
	`, articleID).Scan(&e.ArticleID, &e.Label, &e.Reason, &e.LabeledBy, &e.LabeledAt, &e.Notes, &e.Revision, &supersedes, &e.ContentHash, &e.Source, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return training.Example{}, false, nil
	}
	if err != nil {
		return training.Example{}, false, fmt.Errorf("sqlite: get active label for %s: %w", articleID, err)
	}
	e.Supersedes = int(supersedes.Int64)
	e.SessionID = sessionID.String
	return e, true, nil
}

// ListByArticle implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) ListByArticle(ctx context.Context, articleID string) ([]training.Example, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT article_id, label, reason, labeled_by, labeled_at, notes, revision, supersedes, content_hash, source, session_id
		FROM human_labels WHERE article_id = ? ORDER BY labeled_at, id
	`, articleID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list human labels for %s: %w", articleID, err)
	}
	defer rows.Close()
	return scanExamples(rows)
}

// ListAll implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) ListAll(ctx context.Context) ([]training.Example, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT article_id, label, reason, labeled_by, labeled_at, notes, revision, supersedes, content_hash, source, session_id
		FROM human_labels WHERE active = 1 ORDER BY labeled_at, id
	`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list all human labels: %w", err)
	}
	defer rows.Close()
	return scanExamples(rows)
}

func scanExamples(rows *sql.Rows) ([]training.Example, error) {
	var examples []training.Example
	for rows.Next() {
		var e training.Example
		var supersedes sql.NullInt64
		var sessionID sql.NullString
		var labeledAt sqliteNullTime
		if err := rows.Scan(&e.ArticleID, &e.Label, &e.Reason, &e.LabeledBy, &labeledAt, &e.Notes, &e.Revision, &supersedes, &e.ContentHash, &e.Source, &sessionID); err != nil {
			return nil, fmt.Errorf("sqlite: scan human label: %w", err)
		}
		if labeledAt.Valid {
			e.LabeledAt = labeledAt.Time
		}
		e.Supersedes = int(supersedes.Int64)
		e.SessionID = sessionID.String
		examples = append(examples, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate human labels: %w", err)
	}
	return examples, nil
}

// ListEditorialExamples implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) ListEditorialExamples(ctx context.Context) ([]training.EditorialExample, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT article_id, label, reason, labeled_by, labeled_at, notes,
			title, body, source_id, source_type, domain, content_type,
			published_at, fetched_at, cluster_id, author, external_id, original_url, canonical_url,
			classifier_version, dictionary_version, revision, supersedes, content_hash, source, session_id
		FROM editorial_examples WHERE active = 1 ORDER BY labeled_at, id
	`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list editorial examples: %w", err)
	}
	defer rows.Close()

	var examples []training.EditorialExample
	for rows.Next() {
		var e training.EditorialExample
		var labeledAt, publishedAt, fetchedAt sqliteNullTime
		var supersedes sql.NullInt64
		var sessionID sql.NullString
		if err := rows.Scan(
			&e.ArticleID, &e.Label, &e.Reason, &e.LabeledBy, &labeledAt, &e.Notes,
			&e.Title, &e.Body, &e.SourceID, &e.SourceType, &e.Domain, &e.ContentType,
			&publishedAt, &fetchedAt, &e.ClusterID, &e.Author, &e.ExternalID, &e.OriginalURL, &e.CanonicalURL,
			&e.ClassifierVersion, &e.DictionaryVersion, &e.Revision, &supersedes, &e.ContentHash, &e.Source, &sessionID,
		); err != nil {
			return nil, fmt.Errorf("sqlite: scan editorial example: %w", err)
		}
		if labeledAt.Valid {
			e.LabeledAt = labeledAt.Time
		}
		if publishedAt.Valid {
			e.PublishedAt = publishedAt.Time
		}
		if fetchedAt.Valid {
			e.FetchedAt = fetchedAt.Time
		}
		e.Supersedes = int(supersedes.Int64)
		e.SessionID = sessionID.String
		examples = append(examples, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate editorial examples: %w", err)
	}
	return examples, nil
}

// CountEditorialExamples implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) CountEditorialExamples(ctx context.Context) (int, error) {
	var n int
	if err := executor(ctx, r.db).QueryRowContext(ctx, `SELECT COUNT(1) FROM editorial_examples`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite: count editorial examples: %w", err)
	}
	return n, nil
}

// ListUnresolvedConflicts implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) ListUnresolvedConflicts(ctx context.Context) ([]training.Conflict, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT id, table_name, article_id, detail, detected_at FROM human_label_conflicts
		WHERE resolved_at IS NULL ORDER BY detected_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list unresolved conflicts: %w", err)
	}
	defer rows.Close()

	var conflicts []training.Conflict
	for rows.Next() {
		var c training.Conflict
		if err := rows.Scan(&c.ID, &c.TableName, &c.ArticleID, &c.Detail, &c.DetectedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan conflict: %w", err)
		}
		conflicts = append(conflicts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate conflicts: %w", err)
	}
	return conflicts, nil
}

// CountUnresolvedConflicts implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) CountUnresolvedConflicts(ctx context.Context) (int, error) {
	var n int
	if err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT article_id) FROM human_label_conflicts WHERE resolved_at IS NULL
	`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite: count unresolved conflicts: %w", err)
	}
	return n, nil
}

// ResolveConflict implements ports/out.HumanLabelRepository.
func (r *HumanLabelRepository) ResolveConflict(ctx context.Context, articleID string, e training.Example) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE human_label_conflicts SET resolved_at = ? WHERE article_id = ? AND resolved_at IS NULL`, sqliteTime(e.LabeledAt), articleID)
		if err != nil {
			return fmt.Errorf("sqlite: mark conflicts resolved for %s: %w", articleID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("sqlite: resolve conflict for %s: no unresolved conflict found", articleID)
		}
		// A conflict can outlive its article: migration 0026 detects
		// ambiguity in editorial_examples independently of human_labels,
		// and editorial_examples has no FK to articles, so a
		// retention-purged article can leave an unresolved conflict with
		// no articles row to snapshot content from. insertRevision needs
		// that row (it fails with "no such article" without one), which
		// would dead-end this article's conflict forever. Resolve it
		// anyway, without a new active revision: the article's content is
		// gone, so there is nothing to add to the training corpus no
		// matter which label would have "won" -- resolving here means
		// acknowledging the conflict and stopping train export's warning,
		// not producing an exportable label.
		var exists int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM articles WHERE id = ?`, articleID).Scan(&exists); {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("sqlite: check article %s for conflict resolution: %w", articleID, err)
		}
		// Deliberately revision 1, not max(existing revision)+1: an
		// article with an unresolved conflict has zero active rows, so
		// this is -- from ExportDataset's and GetActive's perspective --
		// the article's first real decision. Its pre-existing (all
		// inactive) legacy rows keep whatever revision numbers the
		// 0026 backfill assigned them; there is no uniqueness
		// constraint on (article_id, revision) itself, only on
		// (article_id) WHERE active=1, so this never collides with the
		// real constraint. `label history <article-id>` still shows
		// every row, ordered by labeled_at, so nothing is hidden.
		return r.insertRevision(ctx, tx, e, 1, 0)
	})
}

// ImportOutcome is what ImportEditorialExample did with one snapshot.
type ImportOutcome int

const (
	// ImportInserted: the article had no active label; the snapshot is now it.
	ImportInserted ImportOutcome = iota
	// ImportUnchanged: the active label already agrees; nothing written.
	ImportUnchanged
	// ImportConflict: an active label disagrees and override was false;
	// nothing written.
	ImportConflict
	// ImportSuperseded: an active label disagreed and override was true; it
	// was superseded (kept as history) by the snapshot's new revision.
	ImportSuperseded
)

// ImportEditorialExample writes a previously captured label snapshot back
// into editorial_examples — the recovery path for labels whose article
// row retention already purged (so Label/Relabel, which snapshot from
// articles, cannot be used) and whose only copy lives in an old database
// backup or a reviewed dataset file. It never deletes: a disagreeing
// active label is either left alone (override=false, ImportConflict) or
// superseded like Relabel does (override=true), including its
// human_labels row so the two tables never keep contradicting active
// labels. The snapshot's own content hash is recomputed when empty.
func (r *HumanLabelRepository) ImportEditorialExample(ctx context.Context, e training.EditorialExample, override bool) (ImportOutcome, error) {
	var outcome ImportOutcome
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		var activeLabel string
		var activeRevision int
		err := tx.QueryRowContext(ctx, `SELECT label, revision FROM editorial_examples WHERE article_id = ? AND active = 1`, e.ArticleID).Scan(&activeLabel, &activeRevision)
		hasActive := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlite: import %s: read active example: %w", e.ArticleID, err)
		}
		switch {
		case hasActive && activeLabel == e.Label:
			outcome = ImportUnchanged
			return nil
		case hasActive && !override:
			outcome = ImportConflict
			return nil
		}

		supersedes := any(nil)
		if hasActive {
			if _, err := tx.ExecContext(ctx, `UPDATE editorial_examples SET active = 0 WHERE article_id = ? AND active = 1`, e.ArticleID); err != nil {
				return fmt.Errorf("sqlite: import %s: supersede editorial_examples: %w", e.ArticleID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE human_labels SET active = 0 WHERE article_id = ? AND active = 1`, e.ArticleID); err != nil {
				return fmt.Errorf("sqlite: import %s: supersede human_labels: %w", e.ArticleID, err)
			}
			supersedes = activeRevision
		}
		var revision int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM editorial_examples WHERE article_id = ?`, e.ArticleID).Scan(&revision); err != nil {
			return fmt.Errorf("sqlite: import %s: next revision: %w", e.ArticleID, err)
		}
		contentHash := e.ContentHash
		if contentHash == "" {
			sum := sha256.Sum256([]byte(e.Title + "\x00" + e.Body))
			contentHash = hex.EncodeToString(sum[:])
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO editorial_examples (
				article_id, label, reason, labeled_by, labeled_at, notes,
				title, body, source_id, source_type, domain, content_type,
				published_at, fetched_at, cluster_id, author, external_id, original_url, canonical_url,
				classifier_version, dictionary_version, revision, active, supersedes, content_hash, source, session_id
			)
			VALUES (?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?,?, ?,?,?,1,?,?,?,?)
		`, e.ArticleID, e.Label, e.Reason, e.LabeledBy, e.LabeledAt, e.Notes,
			e.Title, e.Body, e.SourceID, e.SourceType, e.Domain, e.ContentType,
			nullableTime(e.PublishedAt), nullableTime(e.FetchedAt), e.ClusterID, e.Author, e.ExternalID, e.OriginalURL, e.CanonicalURL,
			e.ClassifierVersion, e.DictionaryVersion, revision, supersedes, contentHash, e.Source, nullableString(e.SessionID)); err != nil {
			return fmt.Errorf("sqlite: import %s: insert editorial_examples: %w", e.ArticleID, err)
		}
		outcome = ImportInserted
		if hasActive {
			outcome = ImportSuperseded
		}
		return nil
	})
	return outcome, err
}
