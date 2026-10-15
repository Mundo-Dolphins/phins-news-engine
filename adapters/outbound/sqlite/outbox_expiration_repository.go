package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// OutboxExpirationRepository implements the safe backlog-expiration port.
type OutboxExpirationRepository struct{ db *sql.DB }

// NewOutboxExpirationRepository constructs the SQLite adapter.
func NewOutboxExpirationRepository(db *sql.DB) *OutboxExpirationRepository {
	return &OutboxExpirationRepository{db: db}
}

var _ appout.OutboxExpirationRepository = (*OutboxExpirationRepository)(nil)

// ListExpirationCandidates returns only old pending/retryable rows.
func (r *OutboxExpirationRepository) ListExpirationCandidates(ctx context.Context, filter appout.OutboxExpirationFilter) ([]appout.OutboxExpirationCandidate, error) {
	where := []string{"po.status IN ('pending','retryable')"}
	expiredBefore := filter.ExpiredBefore
	if expiredBefore.IsZero() {
		expiredBefore = filter.PublishedBefore
	}
	args := []any{}
	if !expiredBefore.IsZero() {
		where = append(where, "(po.expires_at IS NOT NULL AND po.expires_at <= ? OR po.expires_at IS NULL AND a.published_at IS NOT NULL AND a.published_at < ?)")
		args = append(args, sqliteTime(expiredBefore), sqliteTime(expiredBefore))
	}
	if !filter.PublishedBefore.IsZero() {
		where = append(where, "(po.expires_at IS NOT NULL OR (a.published_at IS NOT NULL AND a.published_at < ?))")
		args = append(args, sqliteTime(filter.PublishedBefore))
	}
	if filter.ContentType != "" {
		where = append(where, "a.content_type = ?")
		args = append(args, string(filter.ContentType))
	}
	if filter.SourceID != "" {
		where = append(where, "a.source_id = ?")
		args = append(args, filter.SourceID)
	}
	args = append(args, filter.Limit)
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT po.id, po.article_id, po.status, a.published_at, po.expires_at, a.content_type, a.source_id, a.title
		FROM publication_outbox po JOIN articles a ON a.id = po.article_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY a.published_at ASC, po.id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list outbox expiration candidates: %w", err)
	}
	defer rows.Close()
	var candidates []appout.OutboxExpirationCandidate
	for rows.Next() {
		var c appout.OutboxExpirationCandidate
		var status string
		var publishedAt sqliteNullTime
		var expiresAt sqliteNullTime
		if err := rows.Scan(&c.OutboxID, &c.ArticleID, &status, &publishedAt, &expiresAt, &c.ContentType, &c.SourceID, &c.Title); err != nil {
			return nil, fmt.Errorf("sqlite: scan outbox expiration candidate: %w", err)
		}
		c.Status = publishing.OutboxStatus(status)
		c.PublishedAt = publishedAt.Time
		if expiresAt.Valid {
			c.ExpiresAt = expiresAt.Time
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate outbox expiration candidates: %w", err)
	}
	return candidates, nil
}

// ExpireClaimable moves one still-claimable row to dead with a guarded update.
func (r *OutboxExpirationRepository) ExpireClaimable(ctx context.Context, outboxID int64, articleID, reason string, now time.Time) error {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE publication_outbox SET status = 'dead', last_error = ?, claimed_at = NULL, claimed_by = '', updated_at = ?
		WHERE id = ? AND article_id = ? AND status IN ('pending','retryable')`, reason, sqliteTime(now), outboxID, articleID)
	if err != nil {
		return fmt.Errorf("sqlite: expire outbox item %d: %w", outboxID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: expire outbox item %d rows affected: %w", outboxID, err)
	}
	if n != 1 {
		return fmt.Errorf("sqlite: expire outbox item %d: %w", outboxID, appout.ErrNotExpirable)
	}
	return nil
}
