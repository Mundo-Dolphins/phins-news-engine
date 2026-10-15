package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// EditorialCycleSessionCandidateRepository implements
// ports/out.EditorialCycleSessionCandidateRepository against SQLite.
type EditorialCycleSessionCandidateRepository struct {
	db *sql.DB
}

// NewEditorialCycleSessionCandidateRepository builds an
// EditorialCycleSessionCandidateRepository over an already-open,
// already-migrated database.
func NewEditorialCycleSessionCandidateRepository(db *sql.DB) *EditorialCycleSessionCandidateRepository {
	return &EditorialCycleSessionCandidateRepository{db: db}
}

var _ appout.EditorialCycleSessionCandidateRepository = (*EditorialCycleSessionCandidateRepository)(nil)

// SaveAll implements ports/out.EditorialCycleSessionCandidateRepository.
func (r *EditorialCycleSessionCandidateRepository) SaveAll(ctx context.Context, sessionID string, candidates []appout.EditorialCycleSessionCandidate) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, c := range candidates {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO editorial_cycle_session_candidates (session_id, article_id, score, selected_at)
				VALUES (?,?,?,?)
				`, sessionID, c.ArticleID, c.Score, sqliteTime(c.SelectedAt)); err != nil {
				return fmt.Errorf("sqlite: save session candidate %s/%s: %w", sessionID, c.ArticleID, err)
			}
		}
		return nil
	})
}

// ListPending implements ports/out.EditorialCycleSessionCandidateRepository.
func (r *EditorialCycleSessionCandidateRepository) ListPending(ctx context.Context, sessionID string) ([]appout.EditorialCycleSessionCandidate, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT sc.session_id, sc.article_id, sc.score, sc.selected_at
		FROM editorial_cycle_session_candidates sc
		JOIN articles a ON a.id = sc.article_id
		WHERE sc.session_id = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM editorial_review_decisions d
		    WHERE d.session_id = sc.session_id AND d.article_id = sc.article_id
		  )
		ORDER BY sc.score DESC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list pending session candidates for %s: %w", sessionID, err)
	}
	return scanSessionCandidates(rows)
}

// ListFrozen implements ports/out.EditorialCycleSessionCandidateRepository.
func (r *EditorialCycleSessionCandidateRepository) ListFrozen(ctx context.Context, sessionID string) ([]appout.EditorialCycleSessionCandidate, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT session_id, article_id, score, selected_at
		FROM editorial_cycle_session_candidates WHERE session_id = ? ORDER BY score DESC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list frozen session candidates for %s: %w", sessionID, err)
	}
	return scanSessionCandidates(rows)
}

// CountResolvable implements
// ports/out.EditorialCycleSessionCandidateRepository.
func (r *EditorialCycleSessionCandidateRepository) CountResolvable(ctx context.Context, sessionID string) (int, error) {
	var n int
	err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT COUNT(1) FROM editorial_cycle_session_candidates sc
		JOIN articles a ON a.id = sc.article_id WHERE sc.session_id = ?
	`, sessionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlite: count resolvable session candidates for %s: %w", sessionID, err)
	}
	return n, nil
}

func scanSessionCandidates(rows *sql.Rows) ([]appout.EditorialCycleSessionCandidate, error) {
	defer rows.Close()
	var out []appout.EditorialCycleSessionCandidate
	for rows.Next() {
		var c appout.EditorialCycleSessionCandidate
		var selectedAt sqliteNullTime
		if err := rows.Scan(&c.SessionID, &c.ArticleID, &c.Score, &selectedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan session candidate: %w", err)
		}
		if selectedAt.Valid {
			c.SelectedAt = selectedAt.Time
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate session candidates: %w", err)
	}
	return out, nil
}
