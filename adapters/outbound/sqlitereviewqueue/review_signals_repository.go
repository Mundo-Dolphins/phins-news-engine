package sqlitereviewqueue

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

type ReviewSignalsRepository struct {
	db *sql.DB
}

func NewReviewSignalsRepository(db *sql.DB) *ReviewSignalsRepository {
	return &ReviewSignalsRepository{db: db}
}

var _ reviewqueueport.ReviewSignalsRepository = (*ReviewSignalsRepository)(nil)

func (r *ReviewSignalsRepository) CountUnknownEntities(ctx context.Context, articleID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM unknown_entity_occurrences WHERE article_id = ?`, articleID).Scan(&count); err != nil {
		return 0, fmt.Errorf("sqlitereviewqueue: count unknown entities for %s: %w", articleID, err)
	}
	return count, nil
}

func (r *ReviewSignalsRepository) CountPendingDictionaryProposals(ctx context.Context, articleID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM classifier_dictionary_proposal_sources s
		JOIN classifier_dictionary_proposals p ON p.id = s.proposal_id
		WHERE s.article_id = ? AND p.status = 'pending'
	`, articleID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("sqlitereviewqueue: count pending dictionary proposals for %s: %w", articleID, err)
	}
	return count, nil
}
