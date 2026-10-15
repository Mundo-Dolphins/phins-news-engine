package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ClusterPublicationRepository implements
// ports/out.ClusterPublicationRepository against SQLite — the
// persistence-level guarantee that a cluster produces at most one initial
// publication.
type ClusterPublicationRepository struct {
	db *sql.DB
}

// NewClusterPublicationRepository builds a ClusterPublicationRepository
// over an already-open, already-migrated database.
func NewClusterPublicationRepository(db *sql.DB) *ClusterPublicationRepository {
	return &ClusterPublicationRepository{db: db}
}

var _ appout.ClusterPublicationRepository = (*ClusterPublicationRepository)(nil)

// Reserve implements ports/out.ClusterPublicationRepository. The single
// upsert statement is the compare-and-swap: it inserts (no row existed),
// updates (existing row was 'released'), or changes nothing at all
// (existing row is 'reserved'/'confirmed') — RowsAffected distinguishes
// "acquired" from "lost the race", with no error raised for the latter.
func (r *ClusterPublicationRepository) Reserve(ctx context.Context, res appout.ClusterPublicationReservation, now time.Time) (bool, error) {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO event_cluster_publications
			(cluster_id, state, article_id, source_id, member_role, decision_code, policy_version, reason, reserved_at)
		VALUES (?, 'reserved', ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(cluster_id) DO UPDATE SET
			state = 'reserved', article_id = excluded.article_id, source_id = excluded.source_id,
			member_role = excluded.member_role, decision_code = excluded.decision_code,
			policy_version = excluded.policy_version, reason = excluded.reason,
			reserved_at = excluded.reserved_at, confirmed_at = NULL, released_at = NULL
		WHERE event_cluster_publications.state = 'released'
		`, res.ClusterID, res.ArticleID, res.SourceID, res.MemberRole, res.DecisionCode, res.PolicyVersion, res.Reason, sqliteTime(now))
	if err != nil {
		return false, fmt.Errorf("sqlite: reserve cluster publication gate for cluster %s: %w", res.ClusterID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: rows affected reserving cluster %s: %w", res.ClusterID, err)
	}
	return n > 0, nil
}

// Confirm implements ports/out.ClusterPublicationRepository.
func (r *ClusterPublicationRepository) Confirm(ctx context.Context, clusterID, articleID string, outboxID int64, now time.Time) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE event_cluster_publications SET state = 'confirmed', outbox_id = ?, confirmed_at = ?
		WHERE cluster_id = ? AND article_id = ?
		`, outboxID, sqliteTime(now), clusterID, articleID)
	if err != nil {
		return fmt.Errorf("sqlite: confirm cluster publication gate for cluster %s: %w", clusterID, err)
	}
	return nil
}

// Release implements ports/out.ClusterPublicationRepository.
func (r *ClusterPublicationRepository) Release(ctx context.Context, clusterID, articleID string, now time.Time) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE event_cluster_publications SET state = 'released', released_at = ?
		WHERE cluster_id = ? AND article_id = ?
		`, sqliteTime(now), clusterID, articleID)
	if err != nil {
		return fmt.Errorf("sqlite: release cluster publication gate for cluster %s: %w", clusterID, err)
	}
	return nil
}

var _ appout.ClusterPublishedSourceReader = (*ClusterPublicationRepository)(nil)

// PublishedSourceIDs implements ports/out.ClusterPublishedSourceReader.
func (r *ClusterPublicationRepository) PublishedSourceIDs(ctx context.Context, clusterID, excludeArticleID string) ([]string, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT DISTINCT source_id FROM articles
		WHERE current_cluster_id = ? AND id != ?
		  AND status IN ('pending_publication', 'publishing', 'published', 'pending_retry')
		`, clusterID, excludeArticleID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list published sources for cluster %s: %w", clusterID, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlite: scan published source for cluster %s: %w", clusterID, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
