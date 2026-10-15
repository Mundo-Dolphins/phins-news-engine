// adapters/outbound/sqlitereviewqueue/review_queue_repository.go
package sqlitereviewqueue

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

type ReviewQueueRepository struct {
	db *sql.DB
}

func NewReviewQueueRepository(db *sql.DB) *ReviewQueueRepository {
	return &ReviewQueueRepository{db: db}
}

var _ reviewqueueport.ReviewQueueRepository = (*ReviewQueueRepository)(nil)

const sqliteTimeFormat = time.RFC3339Nano

func sqliteTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(sqliteTimeFormat)
}

func (r *ReviewQueueRepository) Upsert(ctx context.Context, e reviewpriority.QueueEntry) error {
	now := sqliteTime(time.Now())
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO review_queue (
			article_id, proposal_id, score, reason, nb_zone, nb_value,
			llm_label, llm_confidence, suggested_label, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(article_id) DO UPDATE SET
			proposal_id=excluded.proposal_id, score=excluded.score, reason=excluded.reason,
			nb_zone=excluded.nb_zone, nb_value=excluded.nb_value, llm_label=excluded.llm_label,
			llm_confidence=excluded.llm_confidence, suggested_label=excluded.suggested_label,
			updated_at=excluded.updated_at
	`,
		e.ArticleID, e.ProposalID, e.Score, e.Reason, string(e.NBZone), e.NBValue,
		e.LLMLabel, e.LLMConfidence, e.SuggestedLabel, now, now,
	)
	if err != nil {
		return fmt.Errorf("sqlitereviewqueue: upsert %s: %w", e.ArticleID, err)
	}
	return nil
}

func (r *ReviewQueueRepository) Delete(ctx context.Context, articleID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM review_queue WHERE article_id = ?`, articleID); err != nil {
		return fmt.Errorf("sqlitereviewqueue: delete %s: %w", articleID, err)
	}
	return nil
}

func (r *ReviewQueueRepository) ListOrderedByScore(ctx context.Context, limit int) ([]reviewpriority.QueueEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT article_id, proposal_id, score, reason, nb_zone, nb_value,
		       llm_label, llm_confidence, suggested_label, created_at, updated_at
		FROM review_queue ORDER BY score DESC, created_at ASC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlitereviewqueue: list: %w", err)
	}
	defer rows.Close()

	var out []reviewpriority.QueueEntry
	for rows.Next() {
		var e reviewpriority.QueueEntry
		var zone, createdAt, updatedAt string
		if err := rows.Scan(&e.ArticleID, &e.ProposalID, &e.Score, &e.Reason, &zone, &e.NBValue,
			&e.LLMLabel, &e.LLMConfidence, &e.SuggestedLabel, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlitereviewqueue: scan: %w", err)
		}
		e.NBZone = editorial.Zone(zone)
		if e.CreatedAt, err = time.Parse(sqliteTimeFormat, createdAt); err != nil {
			return nil, fmt.Errorf("sqlitereviewqueue: parse created_at: %w", err)
		}
		if e.UpdatedAt, err = time.Parse(sqliteTimeFormat, updatedAt); err != nil {
			return nil, fmt.Errorf("sqlitereviewqueue: parse updated_at: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlitereviewqueue: iterate: %w", err)
	}
	return out, nil
}
