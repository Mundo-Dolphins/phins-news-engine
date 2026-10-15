package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ClusterRepository implements ports/out.ClusterRepository against SQLite.
type ClusterRepository struct {
	db *sql.DB
}

// NewClusterRepository builds a ClusterRepository over an already-open,
// already-migrated database.
func NewClusterRepository(db *sql.DB) *ClusterRepository {
	return &ClusterRepository{db: db}
}

var _ appout.ClusterRepository = (*ClusterRepository)(nil)

// CreateCluster implements ports/out.ClusterRepository.
//
// Safe to call from inside an outer UnitOfWork.Do(...) closure: withTx is
// reentrant and joins the in-flight transaction rather than opening a
// second one.
func (r *ClusterRepository) CreateCluster(ctx context.Context, id, articleID string, status clustering.Status, now time.Time) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_clusters (id, representative_article_id, first_seen_at, last_updated_at, member_count, status)
			VALUES (?, ?, ?, ?, 1, ?)
			`, id, articleID, sqliteTime(now), sqliteTime(now), string(status)); err != nil {
			return fmt.Errorf("sqlite: create cluster %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_cluster_members (cluster_id, article_id, joined_at, role) VALUES (?, ?, ?, ?)
			`, id, articleID, sqliteTime(now), string(clustering.MemberRoleFirstReport)); err != nil {
			return fmt.Errorf("sqlite: add member %s to cluster %s: %w", articleID, id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE articles SET current_cluster_id = ? WHERE id = ?`, id, articleID); err != nil {
			return fmt.Errorf("sqlite: set current_cluster_id for %s: %w", articleID, err)
		}
		return nil
	})
}

// AddMember implements ports/out.ClusterRepository.
//
// Safe to call from inside an outer UnitOfWork.Do(...) closure: withTx is
// reentrant and joins the in-flight transaction rather than opening a
// second one.
func (r *ClusterRepository) AddMember(ctx context.Context, clusterID, articleID string, role clustering.MemberRole, score float64, now time.Time) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_cluster_members (cluster_id, article_id, joined_at, role, clustering_score) VALUES (?, ?, ?, ?, ?)
			`, clusterID, articleID, sqliteTime(now), string(role), score); err != nil {
			return fmt.Errorf("sqlite: add member %s to cluster %s: %w", articleID, clusterID, err)
		}
		// status/closed_at are set unconditionally (not "only if
		// currently closed") — a no-op write on an already-open cluster,
		// and the reopen for a closed one. reopened_count only increments
		// when the row actually was closed, via the CASE — this single
		// statement is the whole reopen decision, no separate read
		// first. close_reason is intentionally untouched here — it keeps
		// reading as "the reason for the most recent close" even after a
		// reopen. See ClusterRepository (port) AddMember's doc comment.
		if _, err := tx.ExecContext(ctx, `
			UPDATE event_clusters
			SET member_count = member_count + 1,
			    last_updated_at = ?,
			    status = 'open',
			    closed_at = NULL,
			    reopened_count = reopened_count + CASE WHEN status = 'closed' THEN 1 ELSE 0 END
			WHERE id = ?
			`, sqliteTime(now), clusterID); err != nil {
			return fmt.Errorf("sqlite: update cluster %s member count: %w", clusterID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE articles SET current_cluster_id = ? WHERE id = ?`, clusterID, articleID); err != nil {
			return fmt.Errorf("sqlite: set current_cluster_id for %s: %w", articleID, err)
		}
		return nil
	})
}

// CloseInactiveBatch implements ports/out.ClusterRepository. The WHERE
// clause on the inner SELECT is the entire compare-and-swap: only rows
// still matching status='open' AND last_updated_at <= cutoff at execution
// time are closed, so this is safe to call concurrently with AddMember
// (see the design doc's race analysis) with no separate read or locking.
// <= (not <) matches clusterlifecycle.Eligible's inclusive boundary
// exactly.
func (r *ClusterRepository) CloseInactiveBatch(ctx context.Context, cutoff time.Time, closeReason string, batchSize int, now time.Time) (int, error) {
	res, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE event_clusters
		SET status = 'closed', closed_at = ?, close_reason = ?
		WHERE id IN (
			SELECT id FROM event_clusters
			WHERE status = 'open' AND last_updated_at <= ?
			ORDER BY last_updated_at ASC
			LIMIT ?
		)
		`, sqliteTime(now), closeReason, sqliteTime(cutoff), batchSize)
	if err != nil {
		return 0, fmt.Errorf("sqlite: close inactive clusters batch: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	return int(n), nil
}

// Stats implements ports/out.ClusterRepository. Ages are computed from two
// bounded, index-backed row lookups rather than an application-side scan
// of every open cluster: this metric exists specifically to detect a
// large/growing open set (e.g. right after first deploy, or when the
// closing job itself has regressed), which is exactly the condition under
// which an unbounded Go-side scan would be most expensive and would hold
// this project's single DB connection (SetMaxOpenConns(1)) longest,
// blocking ProcessCycle/PublishOutbox. Both lookups use the (status,
// last_updated_at) index added by migration 0022 — no new migration
// needed. Ages are anchored on last_updated_at (time since last
// activity), not first_seen_at (time since creation): the runbook's
// suggested alert ("open_age_p95_hours exceeding close_after_hours means
// the closing job regressed") only makes sense under the "since last
// activity" reading, which is also what CloseInactiveBatch's own
// eligibility check (status='open' AND last_updated_at <= cutoff) uses.
//
// The age arithmetic itself is done in Go (now.Sub(t)) rather than in SQL
// via julianday(): this driver (modernc.org/sqlite) writes time.Time
// values using Go's default time.Time.String() format
// ("2006-01-02 15:04:05.999999999 -0700 MST") unless a DSN
// _time_format is configured (it isn't, here), and SQLite's julianday()
// cannot parse that format's trailing " -0700 MST" suffix — confirmed
// empirically: julianday(last_updated_at) and julianday(?) with a bound
// time.Time both silently return NULL against this schema. A bare column
// value (not an aggregate expression like MIN(...), which loses the
// driver's TIMESTAMP decltype hint) scans back into time.Time correctly,
// so both queries below select the raw column and let Go do the
// subtraction.
//
// The p95 row is a nearest-rank percentile selected via ORDER BY
// last_updated_at DESC (ages ascending: the most recently active cluster,
// smallest age, first) with OFFSET CAST(0.95 * COUNT(*) AS INT) LIMIT 1 —
// skipping the youngest 95% of open clusters lands on the row that is
// older than exactly 95% of the open set. Both the ORDER BY/LIMIT and the
// COUNT(*) driving the OFFSET are satisfied by the (status,
// last_updated_at) index — an index range scan/seek, not a full-table or
// full-open-set scan.
func (r *ClusterRepository) Stats(ctx context.Context, now time.Time) (appout.ClusterLifecycleStats, error) {
	var stats appout.ClusterLifecycleStats

	row := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END), 0),
			COALESCE(MAX(CASE WHEN status = 'open' THEN member_count END), 0),
			COALESCE(SUM(reopened_count), 0)
		FROM event_clusters
	`)
	if err := row.Scan(&stats.OpenCount, &stats.ClosedCount, &stats.OpenMemberCountMax, &stats.ReopenedTotal); err != nil {
		return stats, fmt.Errorf("sqlite: cluster lifecycle counts: %w", err)
	}

	var oldest, p95 sqliteNullTime
	ageRow := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT
			(SELECT last_updated_at FROM event_clusters WHERE status = 'open' ORDER BY last_updated_at ASC LIMIT 1),
			(SELECT last_updated_at FROM event_clusters WHERE status = 'open' ORDER BY last_updated_at DESC
				LIMIT 1 OFFSET (SELECT CAST(0.95 * COUNT(*) AS INT) FROM event_clusters WHERE status = 'open'))
	`)
	if err := ageRow.Scan(&oldest, &p95); err != nil {
		return stats, fmt.Errorf("sqlite: open cluster ages: %w", err)
	}
	if oldest.Valid {
		stats.OpenAgeMaxSeconds = now.Sub(oldest.Time).Seconds()
	}
	if p95.Valid {
		stats.OpenAgeP95Seconds = now.Sub(p95.Time).Seconds()
	}

	return stats, nil
}

// DeleteClosedOlderThan implements ports/out.ClusterRepository.
func (r *ClusterRepository) DeleteClosedOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := executor(ctx, r.db).ExecContext(ctx, `
		DELETE FROM event_clusters WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at < ?
		`, sqliteTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("sqlite: delete closed clusters older than %s: %w", cutoff, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	return int(n), nil
}
