package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

const (
	// maxBacklogLagSamples bounds how many delivered rows feed the lag
	// percentiles, so a huge window cannot turn the report into a table scan.
	maxBacklogLagSamples = 5000
	// maxBacklogDeferralReasons bounds the deferral breakdown; reason codes
	// are a small closed set, the cap is a guard against label explosion.
	maxBacklogDeferralReasons = 20

	supersededErrorPrefix = "superseded_by_final:"
)

// BacklogSnapshot measures the publication queue as of now over the trailing
// window. It is read-only and every query is bounded by status/time indexes
// (migration 0044) or a LIMIT, and runs in one deferred transaction so the
// figures are mutually consistent without blocking the worker's writes.
//
// Queue age uses publication_outbox.created_at (when the bot enqueued the
// item); lag uses articles.published_at (when the source published it) up to
// sent_at. The two differ on purpose: age says how long the bot has been
// sitting on work, lag says how stale the content was when readers saw it.
func (r *OutboxRepository) BacklogSnapshot(ctx context.Context, now time.Time, window time.Duration) (publishing.BacklogSnapshot, error) {
	snap := publishing.BacklogSnapshot{GeneratedAt: now, WindowSeconds: int64(window / time.Second)}
	sinceTime := now.Add(-window)
	since := sqliteTime(sinceTime)
	until := sqliteTime(now)
	// created_at/updated_at default to CURRENT_TIMESTAMP ("2006-01-02
	// 15:04:05", UTC), while sent_at and event times are RFC 3339. A string
	// comparison across the two formats is wrong on the bound's own date
	// ('T' sorts after ' '), so columns that may hold the default format are
	// compared against a bound in that format.
	sinceDefault := sinceTime.UTC().Format("2006-01-02 15:04:05")

	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT status, lane, COUNT(1) FROM publication_outbox
			WHERE status IN ('pending','retryable','leased','sent_unconfirmed','quarantined')
			GROUP BY status, lane ORDER BY status, lane`)
		if err != nil {
			return fmt.Errorf("sqlite: backlog statuses: %w", err)
		}
		for rows.Next() {
			var c publishing.BacklogStatusCount
			if err := rows.Scan(&c.Status, &c.Lane, &c.Count); err != nil {
				_ = rows.Close()
				return fmt.Errorf("sqlite: scan backlog status: %w", err)
			}
			if c.Status == string(publishing.OutboxPending) || c.Status == string(publishing.OutboxRetryable) {
				snap.BacklogCount += c.Count
			}
			snap.Statuses = append(snap.Statuses, c)
		}
		if err := closeRows(rows); err != nil {
			return fmt.Errorf("sqlite: backlog statuses: %w", err)
		}

		var oldest sqliteNullTime
		err = tx.QueryRowContext(ctx, `
			SELECT created_at FROM publication_outbox
			WHERE status IN ('pending','retryable') ORDER BY created_at ASC LIMIT 1`).Scan(&oldest)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("sqlite: backlog oldest pending: %w", err)
		}
		if oldest.Valid && now.After(oldest.Time) {
			snap.OldestPendingAgeSeconds = int64(now.Sub(oldest.Time) / time.Second)
		}

		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox WHERE created_at >= ?`, sinceDefault).Scan(&snap.Enqueued); err != nil {
			return fmt.Errorf("sqlite: backlog enqueued: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM publication_outbox WHERE status='delivered' AND sent_at >= ? AND sent_at <= ?`, since, until).Scan(&snap.Delivered); err != nil {
			return fmt.Errorf("sqlite: backlog delivered: %w", err)
		}
		// updated_at holds either format (see sinceDefault): the indexed
		// bound is a cheap superset, the exact cut is made on parsed times.
		supRows, err := tx.QueryContext(ctx, `
			SELECT updated_at FROM publication_outbox
			WHERE status='dead' AND updated_at >= ? AND last_error LIKE ? LIMIT ?`,
			sinceDefault, supersededErrorPrefix+"%", maxBacklogLagSamples)
		if err != nil {
			return fmt.Errorf("sqlite: backlog superseded: %w", err)
		}
		for supRows.Next() {
			var updated sqliteNullTime
			if err := supRows.Scan(&updated); err != nil {
				_ = supRows.Close()
				return fmt.Errorf("sqlite: scan backlog superseded: %w", err)
			}
			if updated.Valid && !updated.Time.Before(sinceTime) {
				snap.Superseded++
			}
		}
		if err := closeRows(supRows); err != nil {
			return fmt.Errorf("sqlite: backlog superseded: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM article_processing_events
			WHERE occurred_at >= ? AND event_type = 'outbox_expired'`, since).Scan(&snap.Expired); err != nil {
			return fmt.Errorf("sqlite: backlog expired: %w", err)
		}

		lagRows, err := tx.QueryContext(ctx, `
			SELECT a.published_at, o.sent_at
			FROM publication_outbox o JOIN articles a ON a.id = o.article_id
			WHERE o.status='delivered' AND o.sent_at >= ? AND o.sent_at <= ? AND a.published_at IS NOT NULL
			ORDER BY o.sent_at DESC LIMIT ?`, since, until, maxBacklogLagSamples)
		if err != nil {
			return fmt.Errorf("sqlite: backlog lag: %w", err)
		}
		var lags []time.Duration
		for lagRows.Next() {
			var published, sent sqliteNullTime
			if err := lagRows.Scan(&published, &sent); err != nil {
				_ = lagRows.Close()
				return fmt.Errorf("sqlite: scan backlog lag: %w", err)
			}
			if published.Valid && sent.Valid && !sent.Time.Before(published.Time) {
				lags = append(lags, sent.Time.Sub(published.Time))
			}
		}
		if err := closeRows(lagRows); err != nil {
			return fmt.Errorf("sqlite: backlog lag: %w", err)
		}
		snap.Lag = publishing.NewLagStats(lags)

		defRows, err := tx.QueryContext(ctx, `
			SELECT reason_code, COUNT(1) AS n FROM article_processing_events
			WHERE occurred_at >= ? AND event_type = 'publish_deferred'
			GROUP BY reason_code ORDER BY n DESC, reason_code ASC LIMIT ?`, since, maxBacklogDeferralReasons)
		if err != nil {
			return fmt.Errorf("sqlite: backlog deferrals: %w", err)
		}
		for defRows.Next() {
			var rc publishing.ReasonCount
			if err := defRows.Scan(&rc.Reason, &rc.Count); err != nil {
				_ = defRows.Close()
				return fmt.Errorf("sqlite: scan backlog deferral: %w", err)
			}
			snap.Deferrals = append(snap.Deferrals, rc)
		}
		return closeRows(defRows)
	})
	if err != nil {
		return publishing.BacklogSnapshot{}, err
	}
	return snap, nil
}

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}
