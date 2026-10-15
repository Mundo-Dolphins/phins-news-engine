package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// AliasRepository implements ports/out.AliasRepository against SQLite.
type AliasRepository struct {
	db *sql.DB
}

// NewAliasRepository builds an AliasRepository over an already-open,
// already-migrated database.
func NewAliasRepository(db *sql.DB) *AliasRepository {
	return &AliasRepository{db: db}
}

var _ appout.AliasRepository = (*AliasRepository)(nil)

// FindArticleID implements ports/out.AliasRepository.
func (r *AliasRepository) FindArticleID(ctx context.Context, aliasType appout.AliasType, aliasValue string) (string, error) {
	var articleID string
	err := executor(ctx, r.db).QueryRowContext(
		ctx,
		`SELECT article_id FROM article_aliases WHERE alias_type = ? AND alias_value = ?`,
		string(aliasType), aliasValue,
	).Scan(&articleID)
	if err != nil {
		return "", fmt.Errorf("sqlite: find alias %s/%s: %w", aliasType, aliasValue, err)
	}
	return articleID, nil
}

// Save implements ports/out.AliasRepository.
func (r *AliasRepository) Save(ctx context.Context, aliasType appout.AliasType, aliasValue, articleID string) error {
	res, err := executor(ctx, r.db).ExecContext(
		ctx,
		`INSERT OR IGNORE INTO article_aliases (article_id, alias_type, alias_value) VALUES (?, ?, ?)`,
		articleID, string(aliasType), aliasValue,
	)
	if err != nil {
		return fmt.Errorf("sqlite: save alias %s/%s: %w", aliasType, aliasValue, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: alias %s/%s: %w", aliasType, aliasValue, appout.ErrAliasAlreadyExists)
	}
	return nil
}
