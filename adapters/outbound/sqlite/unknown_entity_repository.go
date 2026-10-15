// adapters/outbound/sqlite/unknown_entity_repository.go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/google/uuid"
)

// UnknownEntityRepository implements ports/out.UnknownEntityRepository
// against SQLite.
type UnknownEntityRepository struct {
	db *sql.DB
}

// NewUnknownEntityRepository builds an UnknownEntityRepository over an
// already-open, already-migrated database.
func NewUnknownEntityRepository(db *sql.DB) *UnknownEntityRepository {
	return &UnknownEntityRepository{db: db}
}

var _ out.UnknownEntityRepository = (*UnknownEntityRepository)(nil)

// SaveOccurrences implements ports/out.UnknownEntityRepository.
func (r *UnknownEntityRepository) SaveOccurrences(ctx context.Context, articleID string, occurrences []eventsignals.UnknownEntity, rulesVersion string, now time.Time) error {
	if len(occurrences) == 0 {
		return nil
	}
	for _, o := range occurrences {
		_, err := executor(ctx, r.db).ExecContext(ctx, `
			INSERT INTO unknown_entity_occurrences
				(id, article_id, candidate_text, normalized_text, possible_type, confidence, reason, field, rules_version, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, uuid.NewString(), articleID, o.Text, o.NormalizedText, o.PossibleType, o.Confidence, o.Reason, o.Field, rulesVersion, sqliteTime(now))
		if err != nil {
			return fmt.Errorf("sqlite: save unknown entity occurrence for article %s: %w", articleID, err)
		}
	}
	return nil
}
