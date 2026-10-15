package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DeduplicationRepository implements ports/out.DeduplicationRepository
// against SQLite.
type DeduplicationRepository struct {
	db *sql.DB
}

// NewDeduplicationRepository builds a DeduplicationRepository over an
// already-open, already-migrated database.
func NewDeduplicationRepository(db *sql.DB) *DeduplicationRepository {
	return &DeduplicationRepository{db: db}
}

var _ appout.DeduplicationRepository = (*DeduplicationRepository)(nil)

// Exists reports whether fingerprint has already been recorded.
func (r *DeduplicationRepository) Exists(ctx context.Context, fingerprint string) (bool, error) {
	var count int
	err := executor(ctx, r.db).QueryRowContext(ctx, `SELECT COUNT(1) FROM exact_fingerprints WHERE fingerprint = ?`, fingerprint).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("sqlite: check fingerprint: %w", err)
	}
	return count > 0, nil
}

// Save records fingerprint as belonging to articleID.
func (r *DeduplicationRepository) Save(ctx context.Context, fingerprint, articleID string) error {
	res, err := executor(ctx, r.db).ExecContext(
		ctx,
		`INSERT OR IGNORE INTO exact_fingerprints (fingerprint, article_id) VALUES (?, ?)`,
		fingerprint, articleID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: save fingerprint %s: %w", fingerprint, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: fingerprint %s: %w", fingerprint, appout.ErrAlreadyExists)
	}
	return nil
}
