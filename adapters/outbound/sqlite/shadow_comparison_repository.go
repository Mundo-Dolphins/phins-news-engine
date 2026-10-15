package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ShadowComparisonRepository implements ports/out.ShadowComparisonRepository
// against SQLite.
type ShadowComparisonRepository struct {
	db *sql.DB
}

// NewShadowComparisonRepository builds a ShadowComparisonRepository over an
// already-open, already-migrated database.
func NewShadowComparisonRepository(db *sql.DB) *ShadowComparisonRepository {
	return &ShadowComparisonRepository{db: db}
}

var _ appout.ShadowComparisonRepository = (*ShadowComparisonRepository)(nil)

// SaveDisagreements implements ports/out.ShadowComparisonRepository.
func (r *ShadowComparisonRepository) SaveDisagreements(ctx context.Context, rows []training.ShadowDisagreement) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, row := range rows {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO editorial_shadow_comparisons (
					run_id, article_id, title, production_version, production_zone, production_score,
					candidate_version, candidate_zone, candidate_score, created_at
				)
				VALUES (?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(run_id, article_id) DO UPDATE SET
					title=excluded.title, production_version=excluded.production_version,
					production_zone=excluded.production_zone, production_score=excluded.production_score,
					candidate_version=excluded.candidate_version, candidate_zone=excluded.candidate_zone,
					candidate_score=excluded.candidate_score, created_at=excluded.created_at
			`, row.RunID, row.ArticleID, row.Title, row.ProductionVersion, row.ProductionZone, row.ProductionScore,
				row.CandidateVersion, row.CandidateZone, row.CandidateScore, sqliteTime(row.CreatedAt)); err != nil {
				return fmt.Errorf("sqlite: save shadow disagreement %s/%s: %w", row.RunID, row.ArticleID, err)
			}
		}
		return nil
	})
}

// ListByRun implements ports/out.ShadowComparisonRepository.
func (r *ShadowComparisonRepository) ListByRun(ctx context.Context, runID string) ([]training.ShadowDisagreement, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT run_id, article_id, title, production_version, production_zone, production_score,
			candidate_version, candidate_zone, candidate_score, created_at
		FROM editorial_shadow_comparisons WHERE run_id = ? ORDER BY created_at DESC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list shadow disagreements for run %s: %w", runID, err)
	}
	defer rows.Close()

	var out []training.ShadowDisagreement
	for rows.Next() {
		var d training.ShadowDisagreement
		var createdAt sqliteNullTime
		if err := rows.Scan(&d.RunID, &d.ArticleID, &d.Title, &d.ProductionVersion, &d.ProductionZone, &d.ProductionScore,
			&d.CandidateVersion, &d.CandidateZone, &d.CandidateScore, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan shadow disagreement row: %w", err)
		}
		if createdAt.Valid {
			d.CreatedAt = createdAt.Time
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate shadow disagreement rows: %w", err)
	}
	return out, nil
}
