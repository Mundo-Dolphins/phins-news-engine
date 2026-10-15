package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DictionaryHistoryRepository implements
// ports/out.DictionaryHistoryRepository against SQLite. It is an
// append-only ledger: Record inserts or updates a row in place by version,
// but no method ever deletes one.
type DictionaryHistoryRepository struct {
	db *sql.DB
}

// NewDictionaryHistoryRepository builds a DictionaryHistoryRepository over
// an already-open, already-migrated database.
func NewDictionaryHistoryRepository(db *sql.DB) *DictionaryHistoryRepository {
	return &DictionaryHistoryRepository{db: db}
}

var _ appout.DictionaryHistoryRepository = (*DictionaryHistoryRepository)(nil)

// Record implements ports/out.DictionaryHistoryRepository.
func (r *DictionaryHistoryRepository) Record(ctx context.Context, rec appout.DictionaryHistoryRecord) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO dictionary_history (version, content_hash, created_at, change_count, source_snapshot_ref, operation_kind, predecessor_version, promoted)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(version) DO UPDATE SET
			content_hash=excluded.content_hash
		`, rec.Version, rec.ContentHash, sqliteTime(rec.CreatedAt), rec.ChangeCount, rec.SourceSnapshotRef, rec.OperationKind, rec.PredecessorVersion, rec.Promoted)
	if err != nil {
		return fmt.Errorf("sqlite: record dictionary history %s: %w", rec.Version, err)
	}
	return nil
}

// List implements ports/out.DictionaryHistoryRepository.
func (r *DictionaryHistoryRepository) List(ctx context.Context) ([]appout.DictionaryHistoryRecord, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT version, content_hash, created_at, change_count, source_snapshot_ref, operation_kind, predecessor_version, promoted
		FROM dictionary_history ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list dictionary history: %w", err)
	}
	defer rows.Close()

	var records []appout.DictionaryHistoryRecord
	for rows.Next() {
		var rec appout.DictionaryHistoryRecord
		var createdAt sqliteNullTime
		if err := rows.Scan(&rec.Version, &rec.ContentHash, &createdAt, &rec.ChangeCount, &rec.SourceSnapshotRef, &rec.OperationKind, &rec.PredecessorVersion, &rec.Promoted); err != nil {
			return nil, fmt.Errorf("sqlite: scan dictionary history: %w", err)
		}
		if createdAt.Valid {
			rec.CreatedAt = createdAt.Time
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate dictionary history: %w", err)
	}
	return records, nil
}

// Get implements ports/out.DictionaryHistoryRepository.
func (r *DictionaryHistoryRepository) Get(ctx context.Context, version string) (appout.DictionaryHistoryRecord, error) {
	var rec appout.DictionaryHistoryRecord
	var createdAt sqliteNullTime
	err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT version, content_hash, created_at, change_count, source_snapshot_ref, operation_kind, predecessor_version, promoted
		FROM dictionary_history WHERE version = ?
	`, version).Scan(&rec.Version, &rec.ContentHash, &createdAt, &rec.ChangeCount, &rec.SourceSnapshotRef, &rec.OperationKind, &rec.PredecessorVersion, &rec.Promoted)
	if err != nil {
		return appout.DictionaryHistoryRecord{}, fmt.Errorf("sqlite: get dictionary history %s: %w", version, err)
	}
	if createdAt.Valid {
		rec.CreatedAt = createdAt.Time
	}
	return rec, nil
}

// SetPromoted implements ports/out.DictionaryHistoryRepository. It
// atomically promotes version and demotes every other version in a single
// statement, so at most one row is ever promoted at a time — mirroring the
// single active/promoted pointer semantics of dictionary_registry. If
// version does not exist, the statement affects zero rows (a true no-op —
// nothing already promoted is disturbed) and SetPromoted returns an error.
func (r *DictionaryHistoryRepository) SetPromoted(ctx context.Context, version string) error {
	res, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE dictionary_history
		SET promoted = (version = ?)
		WHERE EXISTS (SELECT 1 FROM dictionary_history WHERE version = ?)
	`, version, version)
	if err != nil {
		return fmt.Errorf("sqlite: set dictionary history promoted %s: %w", version, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: set dictionary history promoted %s: %w", version, err)
	}
	if affected == 0 {
		return fmt.Errorf("sqlite: set dictionary history promoted %s: version not found", version)
	}
	return nil
}
