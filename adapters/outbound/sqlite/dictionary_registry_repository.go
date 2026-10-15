package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DictionaryRegistryRepository implements
// ports/out.DictionaryRegistryRepository against SQLite.
type DictionaryRegistryRepository struct {
	db *sql.DB
}

// NewDictionaryRegistryRepository builds a DictionaryRegistryRepository
// over an already-open, already-migrated database.
func NewDictionaryRegistryRepository(db *sql.DB) *DictionaryRegistryRepository {
	return &DictionaryRegistryRepository{db: db}
}

var _ appout.DictionaryRegistryRepository = (*DictionaryRegistryRepository)(nil)

// Register implements ports/out.DictionaryRegistryRepository.
func (r *DictionaryRegistryRepository) Register(ctx context.Context, d dictionary.Registration) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO dictionary_registry (version, path, entry_count, promoted_at)
		VALUES (?,?,?,?)
		ON CONFLICT(version) DO UPDATE SET
			path=excluded.path, entry_count=excluded.entry_count, promoted_at=excluded.promoted_at
	`, d.Version, d.Path, d.EntryCount, nullableTime(d.PromotedAt))
	if err != nil {
		return fmt.Errorf("sqlite: register dictionary %s: %w", d.Version, err)
	}
	return nil
}

// Get implements ports/out.DictionaryRegistryRepository.
func (r *DictionaryRegistryRepository) Get(ctx context.Context, version string) (dictionary.Registration, error) {
	var d dictionary.Registration
	var promotedAt sqliteNullTime
	err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT version, path, entry_count, promoted_at FROM dictionary_registry WHERE version = ?
	`, version).Scan(&d.Version, &d.Path, &d.EntryCount, &promotedAt)
	if err != nil {
		return dictionary.Registration{}, fmt.Errorf("sqlite: get dictionary %s: %w", version, err)
	}
	if promotedAt.Valid {
		d.PromotedAt = promotedAt.Time
	}
	return d, nil
}
