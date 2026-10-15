package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ReviewDecisionRepository implements ports/out.ReviewDecisionRepository
// against SQLite.
type ReviewDecisionRepository struct {
	db *sql.DB
}

// NewReviewDecisionRepository builds a ReviewDecisionRepository over an
// already-open, already-migrated database.
func NewReviewDecisionRepository(db *sql.DB) *ReviewDecisionRepository {
	return &ReviewDecisionRepository{db: db}
}

var _ appout.ReviewDecisionRepository = (*ReviewDecisionRepository)(nil)

// Save implements ports/out.ReviewDecisionRepository.
func (r *ReviewDecisionRepository) Save(ctx context.Context, d appout.ReviewDecision) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO editorial_review_decisions (session_id, article_id, action, label, reason, notes, decided_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT (session_id, article_id) DO UPDATE SET
			action = excluded.action, label = excluded.label, reason = excluded.reason,
			notes = excluded.notes, decided_at = excluded.decided_at
		`, d.SessionID, d.ArticleID, d.Action, d.Label, d.Reason, d.Notes, sqliteTime(d.DecidedAt))
	if err != nil {
		return fmt.Errorf("sqlite: save review decision for session %s article %s: %w", d.SessionID, d.ArticleID, err)
	}
	return nil
}

// ListBySession implements ports/out.ReviewDecisionRepository.
func (r *ReviewDecisionRepository) ListBySession(ctx context.Context, sessionID string) ([]appout.ReviewDecision, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT session_id, article_id, action, label, reason, notes, decided_at
		FROM editorial_review_decisions WHERE session_id = ? ORDER BY decided_at
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list review decisions for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var decisions []appout.ReviewDecision
	for rows.Next() {
		var d appout.ReviewDecision
		var decidedAt sqliteNullTime
		if err := rows.Scan(&d.SessionID, &d.ArticleID, &d.Action, &d.Label, &d.Reason, &d.Notes, &decidedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan review decision: %w", err)
		}
		if decidedAt.Valid {
			d.DecidedAt = decidedAt.Time
		}
		decisions = append(decisions, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate review decisions: %w", err)
	}
	return decisions, nil
}

// ListRecentByAction implements ports/out.ReviewDecisionRepository.
func (r *ReviewDecisionRepository) ListRecentByAction(ctx context.Context, action string, since time.Time) ([]string, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT DISTINCT article_id FROM editorial_review_decisions
		WHERE action = ? AND decided_at >= ?
		`, action, sqliteTime(since))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list review decisions by action %s: %w", action, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlite: scan review decision article id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate review decisions by action: %w", err)
	}
	return ids, nil
}
