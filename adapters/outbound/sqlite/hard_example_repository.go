package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// HardExampleRepository implements ports/out.HardExampleRepository
// against SQLite.
type HardExampleRepository struct {
	db *sql.DB
}

// NewHardExampleRepository builds a HardExampleRepository over an
// already-open, already-migrated database.
func NewHardExampleRepository(db *sql.DB) *HardExampleRepository {
	return &HardExampleRepository{db: db}
}

var _ appout.HardExampleRepository = (*HardExampleRepository)(nil)

// Upsert implements ports/out.HardExampleRepository.
func (r *HardExampleRepository) Upsert(ctx context.Context, c training.HardExampleCandidate) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO hard_example_candidates (article_id, kind, nb_score, llm_label, llm_confidence, mined_at, resolved_at)
		VALUES (?,?,?,?,?,?,NULL)
		ON CONFLICT(article_id) DO UPDATE SET
			kind=excluded.kind, nb_score=excluded.nb_score, llm_label=excluded.llm_label,
			llm_confidence=excluded.llm_confidence, mined_at=excluded.mined_at, resolved_at=NULL
	`, c.ArticleID, c.Kind, c.NBScore, c.LLMLabel, c.LLMConfidence, sqliteTime(c.MinedAt))
	if err != nil {
		return fmt.Errorf("sqlite: upsert hard example candidate %s: %w", c.ArticleID, err)
	}
	return nil
}

// UnresolvedCount implements ports/out.HardExampleRepository.
func (r *HardExampleRepository) UnresolvedCount(ctx context.Context) (int, error) {
	var count int
	err := executor(ctx, r.db).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hard_example_candidates WHERE resolved_at IS NULL`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("sqlite: count unresolved hard example candidates: %w", err)
	}
	return count, nil
}

// ListUnresolved implements ports/out.HardExampleRepository.
func (r *HardExampleRepository) ListUnresolved(ctx context.Context, limit int) ([]training.HardExampleCandidate, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT article_id, kind, nb_score, llm_label, llm_confidence, mined_at
		FROM hard_example_candidates WHERE resolved_at IS NULL
		ORDER BY mined_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list unresolved hard example candidates: %w", err)
	}
	defer rows.Close()

	var out []training.HardExampleCandidate
	for rows.Next() {
		var c training.HardExampleCandidate
		var minedAt sqliteNullTime
		if err := rows.Scan(&c.ArticleID, &c.Kind, &c.NBScore, &c.LLMLabel, &c.LLMConfidence, &minedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan hard example candidate: %w", err)
		}
		if minedAt.Valid {
			c.MinedAt = minedAt.Time
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list unresolved hard example candidates: %w", err)
	}
	return out, nil
}

// IsUnresolved implements ports/out.HardExampleRepository.
func (r *HardExampleRepository) IsUnresolved(ctx context.Context, articleID string) (bool, error) {
	var one int
	err := executor(ctx, r.db).QueryRowContext(ctx,
		`SELECT 1 FROM hard_example_candidates WHERE article_id = ? AND resolved_at IS NULL`,
		articleID,
	).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("sqlite: check unresolved hard example candidate %s: %w", articleID, err)
	}
	return true, nil
}

// MarkResolved implements ports/out.HardExampleRepository.
func (r *HardExampleRepository) MarkResolved(ctx context.Context, articleID string, resolvedAt time.Time) error {
	_, err := executor(ctx, r.db).ExecContext(ctx,
		`UPDATE hard_example_candidates SET resolved_at = ? WHERE article_id = ? AND resolved_at IS NULL`,
		sqliteTime(resolvedAt), articleID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: mark hard example candidate %s resolved: %w", articleID, err)
	}
	return nil
}
