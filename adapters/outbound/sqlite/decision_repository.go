package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DecisionRepository implements ports/out.DecisionRepository against
// SQLite.
type DecisionRepository struct {
	db *sql.DB
}

// NewDecisionRepository builds a DecisionRepository over an already-open,
// already-migrated database.
func NewDecisionRepository(db *sql.DB) *DecisionRepository {
	return &DecisionRepository{db: db}
}

var _ appout.DecisionRepository = (*DecisionRepository)(nil)

// Append implements ports/out.DecisionRepository.
func (r *DecisionRepository) Append(ctx context.Context, e appout.Event) error {
	_, err := executor(ctx, r.db).ExecContext(
		ctx, `
		INSERT INTO article_processing_events (
			article_id, occurred_at, from_status, to_status, event_type,
			reason_code, reason_detail, classifier_version, dictionary_version,
			config_snapshot, score, threshold, applied_reject_threshold,
			mode, model_checksum, cycle_id
		) VALUES (?,?,?,?,?, ?,?,?,?, ?,?,?,?, ?,?,?)
	`,
		e.ArticleID, sqliteTime(e.OccurredAt), string(e.FromStatus), string(e.ToStatus), e.EventType,
		e.ReasonCode, e.ReasonDetail, e.ClassifierVersion, e.DictionaryVersion,
		e.ConfigSnapshot, e.Score, e.Threshold, e.RejectThreshold,
		e.Mode, e.ModelChecksum, e.CycleID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: append event for %s: %w", e.ArticleID, err)
	}
	return nil
}

// ListByArticle implements ports/out.DecisionRepository.
func (r *DecisionRepository) ListByArticle(ctx context.Context, articleID string) ([]appout.Event, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT article_id, occurred_at, from_status, to_status, event_type,
			reason_code, reason_detail, classifier_version, dictionary_version,
			config_snapshot, score, threshold, applied_reject_threshold,
			mode, model_checksum, cycle_id
		FROM article_processing_events
		WHERE article_id = ?
		ORDER BY occurred_at, id
	`, articleID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list events for %s: %w", articleID, err)
	}
	defer rows.Close()

	var events []appout.Event
	for rows.Next() {
		var e appout.Event
		var fromStatus, toStatus string
		var occurredAt sqliteNullTime
		if err := rows.Scan(
			&e.ArticleID, &occurredAt, &fromStatus, &toStatus, &e.EventType,
			&e.ReasonCode, &e.ReasonDetail, &e.ClassifierVersion, &e.DictionaryVersion,
			&e.ConfigSnapshot, &e.Score, &e.Threshold, &e.RejectThreshold,
			&e.Mode, &e.ModelChecksum, &e.CycleID,
		); err != nil {
			return nil, fmt.Errorf("sqlite: scan event: %w", err)
		}
		if occurredAt.Valid {
			e.OccurredAt = occurredAt.Time
		}
		e.FromStatus = article.Status(fromStatus)
		e.ToStatus = article.Status(toStatus)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate events: %w", err)
	}
	return events, nil
}

// ListByEventTypesSince implements ports/out.DecisionRepository.
func (r *DecisionRepository) ListByEventTypesSince(ctx context.Context, eventTypes []string, since time.Time, limit int) ([]appout.Event, error) {
	if len(eventTypes) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(eventTypes))
	args := make([]any, 0, len(eventTypes)+2)
	for i, et := range eventTypes {
		placeholders[i] = "?"
		args = append(args, et)
	}
	args = append(args, sqliteTime(since), limit)

	query := fmt.Sprintf(`
		SELECT article_id, occurred_at, from_status, to_status, event_type,
			reason_code, reason_detail, classifier_version, dictionary_version,
			config_snapshot, score, threshold, applied_reject_threshold,
			mode, model_checksum, cycle_id
		FROM article_processing_events
		WHERE event_type IN (%s) AND occurred_at >= ?
		ORDER BY occurred_at DESC
		LIMIT ?
	`, strings.Join(placeholders, ","))

	rows, err := executor(ctx, r.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list events by type since %s: %w", since, err)
	}
	defer rows.Close()

	var events []appout.Event
	for rows.Next() {
		var e appout.Event
		var fromStatus, toStatus string
		var occurredAt sqliteNullTime
		if err := rows.Scan(
			&e.ArticleID, &occurredAt, &fromStatus, &toStatus, &e.EventType,
			&e.ReasonCode, &e.ReasonDetail, &e.ClassifierVersion, &e.DictionaryVersion,
			&e.ConfigSnapshot, &e.Score, &e.Threshold, &e.RejectThreshold,
			&e.Mode, &e.ModelChecksum, &e.CycleID,
		); err != nil {
			return nil, fmt.Errorf("sqlite: scan event: %w", err)
		}
		if occurredAt.Valid {
			e.OccurredAt = occurredAt.Time
		}
		e.FromStatus = article.Status(fromStatus)
		e.ToStatus = article.Status(toStatus)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate events: %w", err)
	}
	return events, nil
}

// DeleteOlderThan implements ports/out.DecisionRepository.
func (r *DecisionRepository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := executor(ctx, r.db).ExecContext(ctx, `DELETE FROM article_processing_events WHERE occurred_at < ?`, sqliteTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("sqlite: delete events older than %s: %w", cutoff, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	return int(n), nil
}
