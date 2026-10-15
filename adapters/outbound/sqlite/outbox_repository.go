package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// OutboxRepository implements ports/out.OutboxRepository against SQLite.
type OutboxRepository struct {
	db      *sql.DB
	liveTTL time.Duration
}

// outboxChronologicalOrder is the total, immutable publication sequence.
// available_at is deliberately absent: it only controls eligibility in the
// WHERE clause, so a policy defer or retry cannot move an article ahead of
// older eligible content. Unknown publication dates sort after known dates
// and use the article creation time, with the outbox id as the final stable
// tie-breaker. Keep this expression identical in ClaimBatch's UPDATE and
// SELECT so the claimed rows are returned in the order in which they were
// selected.
const outboxChronologicalOrder = `
				CASE WHEN a.published_at IS NULL THEN 1 ELSE 0 END ASC,
				a.published_at ASC,
				CASE WHEN a.published_at IS NULL THEN a.created_at END ASC,
				po.id ASC`

// NewOutboxRepository builds an OutboxRepository over an already-open,
// already-migrated database.
func NewOutboxRepository(db *sql.DB) *OutboxRepository {
	return &OutboxRepository{db: db}
}

func (r *OutboxRepository) SetLiveTTL(ttl time.Duration) { r.liveTTL = ttl }

var (
	_ appout.OutboxRepository               = (*OutboxRepository)(nil)
	_ appout.TerminalSupersessionRepository = (*OutboxRepository)(nil)
)

// SupersedeClaimableMatch is deliberately status-guarded. Pending/retryable
// rows are atomically made dead and their articles discarded; leased,
// sent_unconfirmed, delivered and quarantined rows are never touched. The
// caller appends the per-article audit events before its surrounding
// UnitOfWork commits.
func (r *OutboxRepository) SupersedeClaimableMatch(ctx context.Context, matchID, finalArticleID string, now time.Time) ([]appout.SupersededItem, error) {
	var items []appout.SupersededItem
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT po.article_id, a.status
			FROM publication_outbox po
			JOIN articles a ON a.id = po.article_id
			WHERE po.status IN ('pending','retryable')
			  AND po.lane = 'live'
			  AND a.status = 'pending_publication'
			  AND po.article_id != ?
			  AND json_extract(a.metadata, '$.match_state_kind') = 'live'
			  AND json_extract(a.metadata, '$.match_id') = ?
			  AND json_extract(a.metadata, '$.match_phase') IN ('pregame','kickoff','q1','q2','halftime','q3','q4')
		`, finalArticleID, matchID)
		if err != nil {
			return fmt.Errorf("sqlite: find claimable match publications: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item appout.SupersededItem
			if err := rows.Scan(&item.ArticleID, &item.FromStatus); err != nil {
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range items {
			if _, err := tx.ExecContext(ctx, `UPDATE publication_outbox SET status='dead', last_error=?, claimed_at=NULL, claimed_by='', updated_at=? WHERE article_id=? AND status IN ('pending','retryable')`, "superseded_by_final:"+finalArticleID, sqliteTime(now), item.ArticleID); err != nil {
				return fmt.Errorf("sqlite: supersede outbox item for %s: %w", item.ArticleID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE articles SET status='discarded', status_reason=?, updated_at=? WHERE id=? AND status='pending_publication'`, "superseded_by_final:"+finalArticleID, sqliteTime(now), item.ArticleID); err != nil {
				return fmt.Errorf("sqlite: supersede article %s: %w", item.ArticleID, err)
			}
			// A publication accepted by Gate B may hold both reservations.
			// Releasing them here is part of the same transaction so the final
			// article can still use the existing cluster/cooldown rules.
			if _, err := tx.ExecContext(ctx, `UPDATE event_cluster_publications SET state='released', released_at=? WHERE article_id=? AND state='reserved'`, sqliteTime(now), item.ArticleID); err != nil {
				return fmt.Errorf("sqlite: release cluster publication reservation for %s: %w", item.ArticleID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE source_policy_cooldowns SET state='released', released_at=? WHERE article_id=? AND state='reserved'`, sqliteTime(now), item.ArticleID); err != nil {
				return fmt.Errorf("sqlite: release source policy cooldown for %s: %w", item.ArticleID, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// Enqueue implements ports/out.OutboxRepository.
func (r *OutboxRepository) Enqueue(ctx context.Context, articleID, payload string, now time.Time) error {
	return r.EnqueueLane(ctx, articleID, payload, publishing.LaneNormal, now)
}

func (r *OutboxRepository) EnqueueLane(ctx context.Context, articleID, payload string, lane publishing.PublicationLane, now time.Time) error {
	lane = lane.Normalized()
	var liveExpiry any
	if lane == publishing.LaneLive && r.liveTTL > 0 {
		liveExpiry = sqliteTime(now.Add(r.liveTTL))
	}
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT OR IGNORE INTO publication_outbox (article_id, payload, lane, available_at, expires_at)
		SELECT ?, ?, ?, ?, CASE
			WHEN a.published_at IS NOT NULL AND a.freshness_max_age_used > 0
			THEN strftime('%Y-%m-%dT%H:%M:%fZ', a.published_at, printf('+%d seconds', a.freshness_max_age_used))
			ELSE NULL
		END
		FROM articles a WHERE a.id = ?
		`, articleID, payload, string(lane), sqliteTime(now), articleID)
	if err != nil {
		return fmt.Errorf("sqlite: enqueue outbox item for %s: %w", articleID, err)
	}
	if lane == publishing.LaneLive && r.liveTTL > 0 {
		_, err = executor(ctx, r.db).ExecContext(ctx, `UPDATE publication_outbox SET expires_at=? WHERE article_id=? AND lane='live' AND status='pending'`, liveExpiry, articleID)
	}
	return err
}

// ClaimBatch implements ports/out.OutboxRepository. It always runs in its
// own transaction (reclaim + claim + select must be atomic), independent
// of any caller UnitOfWork.
//
// Why this must stay one withTx call, even though this package's *sql.DB is
// opened with SetMaxOpenConns(1) (see infrastructure/sqlite.Open): with a
// single connection, SQLite already serializes every individual statement,
// and the claim UPDATE's own "WHERE status = 'pending'" predicate is
// self-enforcing regardless of transaction wrapping — so splitting these
// three statements into three separate executor(ctx, r.db) calls would NOT,
// on its own, let two callers return the same row from ClaimBatch. (Verified
// empirically: 1500 concurrent goroutines against a hand-rolled
// split-statement clone of this method, run under -race with an
// operationally-absurd staleAfter of 200µs, produced zero double-claims —
// the reclaim predicate `claimed_at < now-staleAfter` is time-gated by the
// caller-supplied, real wall-clock `now`, so a row can only become eligible
// for reclaim after genuine elapsed time >= staleAfter, which is always
// orders of magnitude longer than the sub-millisecond gap between two
// sequential statements on one connection; a caller's own claim+select pair
// reliably completes and returns its rows long before anyone else could
// legitimately reclaim them.)
//
// withTx is reentrant (see executor.go): calling ClaimBatch from inside an
// outer UnitOfWork.Do(...) closure joins that transaction instead of
// deadlocking the single-connection pool, so nesting is safe — it is just
// not something ClaimBatch's only caller (PublishOutbox) currently does.
//
// The real reason to keep this atomic is crash/cancellation consistency,
// not double-claim prevention: without an encompassing transaction, each
// statement commits (autocommits) the instant it runs. If the process
// crashes, or ctx is canceled, between the claim UPDATE and the SELECT, the
// claim UPDATE has already persisted — rows are left status='claimed',
// claimed_by=workerID in the database, but the caller got an error (or
// never ran) and has no record of "owning" them. Those rows are then
// silently stranded until the next staleAfter-gated reclaim rescues them,
// instead of going back to 'pending' immediately as they would on rollback.
// That violates the implicit contract "if ClaimBatch returns an error, no
// state changed" that callers rely on for retry logic. withTx gives us that
// all-or-nothing guarantee independent of connection-pool size, so it also
// protects us for free if MaxOpenConns is ever raised (e.g. a future
// network-backed store) or this code is reused against a different driver.
func (r *OutboxRepository) ClaimBatch(ctx context.Context, workerID string, limit int, staleAfter time.Duration, now time.Time) ([]appout.OutboxItem, error) {
	var items []appout.OutboxItem
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		staleCutoff := now.Add(-staleAfter)
		if _, err := tx.ExecContext(ctx, `
			UPDATE publication_outbox SET status = 'pending', claimed_at = NULL, claimed_by = ''
			WHERE status = 'leased' AND claimed_at < ?
			`, sqliteTime(staleCutoff)); err != nil {
			return fmt.Errorf("sqlite: reclaim stale outbox items: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE publication_outbox SET status = 'leased', claimed_at = ?, claimed_by = ?
			WHERE id IN (
				WITH normal_candidates AS (
					SELECT po.id, ROW_NUMBER() OVER (ORDER BY `+outboxChronologicalOrder+`) AS rn FROM publication_outbox po
					JOIN articles a ON a.id = po.article_id
					WHERE po.lane = 'normal' AND po.status IN ('pending', 'retryable') AND po.available_at <= ?
					  AND (po.expires_at IS NULL OR po.expires_at > ?)
					ORDER BY `+outboxChronologicalOrder+` LIMIT ?
				), live_candidates AS (
					SELECT po.id, ROW_NUMBER() OVER (ORDER BY `+outboxChronologicalOrder+`) AS rn FROM publication_outbox po
					JOIN articles a ON a.id = po.article_id
					WHERE po.lane = 'live' AND po.status IN ('pending', 'retryable') AND po.available_at <= ?
					  AND (po.expires_at IS NULL OR po.expires_at > ?)
					  AND NOT EXISTS (
						SELECT 1 FROM publication_outbox prior
						JOIN articles pa ON pa.id = prior.article_id
						WHERE prior.lane = 'live' AND prior.status IN ('pending','retryable','leased')
						  AND json_extract(pa.metadata, '$.match_id') = json_extract(a.metadata, '$.match_id')
						  AND json_extract(a.metadata, '$.match_id') IS NOT NULL
						  AND prior.id < po.id
					  )
					ORDER BY `+outboxChronologicalOrder+` LIMIT ?
				), candidates AS (
					SELECT id, rn, 0 AS lane_order FROM normal_candidates
					UNION ALL SELECT id, rn, 1 AS lane_order FROM live_candidates
				)
				SELECT id FROM candidates ORDER BY rn, lane_order LIMIT ?
			)
			`, sqliteTime(now), workerID, sqliteTime(now), sqliteTime(now), limit, sqliteTime(now), sqliteTime(now), limit, limit); err != nil {
			return fmt.Errorf("sqlite: claim outbox batch: %w", err)
		}

		rows, err := tx.QueryContext(ctx, `
			SELECT po.id, po.article_id, po.payload, po.status, po.available_at, po.expires_at, po.claimed_at, po.claimed_by, po.attempt_count, po.last_error,
			       po.lane,
			       po.sent_message_id, po.sent_at, po.ambiguous_reason
			FROM publication_outbox po
			JOIN articles a ON a.id = po.article_id
			WHERE po.status = 'leased' AND po.claimed_by = ?
			ORDER BY `+outboxChronologicalOrder+`
			`, workerID)
		if err != nil {
			return fmt.Errorf("sqlite: select claimed outbox items: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			it, err := scanOutboxItem(rows)
			if err != nil {
				return fmt.Errorf("sqlite: scan outbox item: %w", err)
			}
			items = append(items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ClaimExpired implements ports/out.OutboxRepository. It leases up to limit
// pending/retryable rows whose known expires_at has passed — regardless of
// available_at, since a policy defer routinely pushes it past the deadline —
// so the worker can expire them through its audited path. ClaimBatch never
// selects these rows, so without this sweep they would stay pending forever.
// Rows with NULL expires_at are left to the operator `outbox expire` tool
// (ADR 0007).
func (r *OutboxRepository) ClaimExpired(ctx context.Context, workerID string, limit int, now time.Time) ([]appout.OutboxItem, error) {
	var items []appout.OutboxItem
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		claimedAt := sqliteTime(now)
		if _, err := tx.ExecContext(ctx, `
			UPDATE publication_outbox SET status = 'leased', claimed_at = ?, claimed_by = ?
			WHERE id IN (
				SELECT id FROM publication_outbox
				WHERE status IN ('pending', 'retryable') AND expires_at IS NOT NULL AND expires_at <= ?
				ORDER BY expires_at ASC, id ASC LIMIT ?
			)
			`, claimedAt, workerID, sqliteTime(now), limit); err != nil {
			return fmt.Errorf("sqlite: claim expired outbox items: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT id, article_id, payload, status, available_at, expires_at, claimed_at, claimed_by, attempt_count, last_error,
			       lane,
			       sent_message_id, sent_at, ambiguous_reason
			FROM publication_outbox
			WHERE status = 'leased' AND claimed_by = ? AND claimed_at = ? AND expires_at IS NOT NULL AND expires_at <= ?
			ORDER BY expires_at ASC, id ASC
			`, workerID, claimedAt, sqliteTime(now))
		if err != nil {
			return fmt.Errorf("sqlite: select expired outbox items: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			it, err := scanOutboxItem(rows)
			if err != nil {
				return fmt.Errorf("sqlite: scan expired outbox item: %w", err)
			}
			items = append(items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// scanOutboxItem scans one row of the standard 14-column outbox projection
// (id, article_id, payload, status, available_at, expires_at, claimed_at, claimed_by,
// attempt_count, last_error, sent_message_id, sent_at, ambiguous_reason)
// into an appout.OutboxItem.
func scanOutboxItem(row rowScanner) (appout.OutboxItem, error) {
	var it appout.OutboxItem
	var status string
	var availableAt sqliteNullTime
	var expiresAt sqliteNullTime
	var claimedAt sqliteNullTime
	var sentMessageID sql.NullString
	var lane string
	var sentAt sqliteNullTime
	var ambiguousReason sql.NullString
	if err := row.Scan(
		&it.ID, &it.ArticleID, &it.Payload, &status, &availableAt, &expiresAt, &claimedAt, &it.ClaimedBy, &it.AttemptCount, &it.LastError,
		&lane, &sentMessageID, &sentAt, &ambiguousReason,
	); err != nil {
		return appout.OutboxItem{}, err
	}
	it.Status = publishing.OutboxStatus(status)
	it.Lane = publishing.PublicationLane(lane).Normalized()
	if availableAt.Valid {
		it.AvailableAt = availableAt.Time
	}
	if expiresAt.Valid {
		it.ExpiresAt = expiresAt.Time
	}
	if claimedAt.Valid {
		it.ClaimedAt = claimedAt.Time
	}
	if sentMessageID.Valid {
		it.SentMessageID = sentMessageID.String
	}
	if sentAt.Valid {
		it.SentAt = sentAt.Time
	}
	if ambiguousReason.Valid {
		it.AmbiguousReason = ambiguousReason.String
	}
	return it, nil
}

// RecordSentUnconfirmed durably records whatever evidence a publish
// attempt produced — a message_id on a confirmed send, or an empty
// message_id with ambiguousReason set on a transport/read/parse failure
// of unknown outcome — moving item id from 'leased' to
// 'sent_unconfirmed'. Called with the caller's plain (non-UnitOfWork)
// context so it commits immediately, on its own, before any further
// bookkeeping: this single small write is the crash-safety anchor the
// delivery guarantee depends on. Idempotent to call again on an already
// sent_unconfirmed row (e.g. Reconcile updating a matched message_id).
//
// If the guard matches no row the item was moved out from under this
// in-flight send and appout.ErrLeaseLost is returned: a silent no-op here
// would leave no durable evidence that Telegram may already hold the
// message while the caller carries on as if it had been recorded — the
// exact blind-resend window ADR 0030 exists to close.
func (r *OutboxRepository) RecordSentUnconfirmed(ctx context.Context, id int64, messageID, ambiguousReason string, now time.Time) error {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE publication_outbox
		SET status = 'sent_unconfirmed', sent_message_id = ?, ambiguous_reason = ?, sent_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('leased', 'sent_unconfirmed')
		`, messageID, ambiguousReason, sqliteTime(now), sqliteTime(now), id)
	if err != nil {
		return fmt.Errorf("sqlite: record sent-unconfirmed for outbox item %d: %w", id, err)
	}
	return guardMatchedARow(result, fmt.Sprintf("record sent-unconfirmed for outbox item %d", id))
}

// MarkDelivered implements ports/out.OutboxRepository. Like
// RecordSentUnconfirmed it reports appout.ErrLeaseLost rather than silently
// no-op'ing when its status guard matches no row — a delivered item whose
// row never actually moved to 'delivered' would stay claimable, and be
// resent.
func (r *OutboxRepository) MarkDelivered(ctx context.Context, id int64, now time.Time) error {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE publication_outbox SET status = 'delivered', updated_at = ?
		WHERE id = ? AND status IN ('leased', 'sent_unconfirmed')
		`, sqliteTime(now), id)
	if err != nil {
		return fmt.Errorf("sqlite: mark outbox item %d delivered: %w", id, err)
	}
	return guardMatchedARow(result, fmt.Sprintf("mark outbox item %d delivered", id))
}

// guardMatchedARow turns a status-guarded UPDATE that changed nothing into
// appout.ErrLeaseLost, mirroring what Repair does with ErrNotRepairable.
func guardMatchedARow(result sql.Result, what string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: %s rows affected: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: %s: %w", what, appout.ErrLeaseLost)
	}
	return nil
}

// MarkRetryable implements ports/out.OutboxRepository.
func (r *OutboxRepository) MarkRetryable(ctx context.Context, id int64, errMsg string, nextAvailableAt time.Time, dead bool, now time.Time) error {
	status := "retryable"
	if dead {
		status = "dead"
	}
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE publication_outbox
		SET status = ?, available_at = ?, attempt_count = attempt_count + 1, last_error = ?,
			claimed_at = NULL, claimed_by = '', sent_message_id = '', ambiguous_reason = '', updated_at = ?
		WHERE id = ? AND status IN ('leased', 'sent_unconfirmed')
		`, status, sqliteTime(nextAvailableAt), errMsg, sqliteTime(now), id)
	if err != nil {
		return fmt.Errorf("sqlite: mark outbox item %d retryable: %w", id, err)
	}
	return nil
}

// ExpireLeased implements ports/out.OutboxRepository. This pre-network
// transition deliberately does not increment attempt_count.
func (r *OutboxRepository) ExpireLeased(ctx context.Context, id int64, reason string, now time.Time) error {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE publication_outbox
		SET status = 'dead', last_error = ?, claimed_at = NULL, claimed_by = '', updated_at = ?
		WHERE id = ? AND status = 'leased'
		`, reason, sqliteTime(now), id)
	if err != nil {
		return fmt.Errorf("sqlite: expire leased outbox item %d: %w", id, err)
	}
	return guardMatchedARow(result, fmt.Sprintf("expire leased outbox item %d", id))
}

// Quarantine implements ports/out.OutboxRepository. Only a
// sent_unconfirmed row can be quarantined — Reconcile is the only caller,
// and it only ever calls this after exhausting deterministic resolution
// for a sent_unconfirmed item.
func (r *OutboxRepository) Quarantine(ctx context.Context, id int64, reason string, now time.Time) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE publication_outbox
		SET status = 'quarantined', ambiguous_reason = ?, quarantined_at = ?, updated_at = ?
		WHERE id = ? AND status = 'sent_unconfirmed'
		`, reason, sqliteTime(now), sqliteTime(now), id)
	if err != nil {
		return fmt.Errorf("sqlite: quarantine outbox item %d: %w", id, err)
	}
	return nil
}

// ListSentUnconfirmed implements ports/out.OutboxRepository.
func (r *OutboxRepository) ListSentUnconfirmed(ctx context.Context, limit int, now time.Time) ([]appout.OutboxItem, error) {
	return r.listByStatus(ctx, "sent_unconfirmed", "sent_at", limit)
}

// ListQuarantined implements ports/out.OutboxRepository.
func (r *OutboxRepository) ListQuarantined(ctx context.Context, limit int) ([]appout.OutboxItem, error) {
	return r.listByStatus(ctx, "quarantined", "quarantined_at", limit)
}

// listByStatus is the shared implementation behind ListSentUnconfirmed and
// ListQuarantined: both list a single status, ordered oldest-first by
// whichever timestamp column marks when that status was entered. orderCol
// is always one of the two hardcoded string literals above, never
// caller/user input, so building the ORDER BY clause with fmt.Sprintf is
// safe (SQL placeholders can't parameterize a column name).
func (r *OutboxRepository) listByStatus(ctx context.Context, status, orderCol string, limit int) ([]appout.OutboxItem, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, fmt.Sprintf(`
		SELECT id, article_id, payload, status, available_at, expires_at, claimed_at, claimed_by, attempt_count, last_error,
		       lane,
		       sent_message_id, sent_at, ambiguous_reason
		FROM publication_outbox
		WHERE status = ?
		ORDER BY %s ASC
		LIMIT ?
	`, orderCol), status, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list outbox items with status %s: %w", status, err)
	}
	defer rows.Close()

	var items []appout.OutboxItem
	for rows.Next() {
		it, err := scanOutboxItem(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan outbox item: %w", err)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Repair implements ports/out.OutboxRepository. It only ever transitions
// an item currently 'sent_unconfirmed' or 'quarantined' AND owned by
// articleID — the WHERE clause on every branch enforces both — and
// reports ErrNotRepairable (RowsAffected == 0) if id is in neither state
// or articleID doesn't match, so a double-repair, a stale operator
// command, or an --article-id copy/paste slip all fail loudly instead of
// silently no-op'ing or mutating the wrong article.
func (r *OutboxRepository) Repair(ctx context.Context, id int64, articleID string, action publishing.RepairAction, reason string, now time.Time) error {
	var result sql.Result
	var err error
	switch action {
	case publishing.RepairDeliver:
		result, err = executor(ctx, r.db).ExecContext(ctx, `
			UPDATE publication_outbox SET status = 'delivered', last_error = ?, updated_at = ?
			WHERE id = ? AND article_id = ? AND status IN ('sent_unconfirmed', 'quarantined')
			`, reason, sqliteTime(now), id, articleID)
	case publishing.RepairRetry:
		result, err = executor(ctx, r.db).ExecContext(ctx, `
			UPDATE publication_outbox SET status = 'retryable', available_at = ?, last_error = ?,
				sent_message_id = '', ambiguous_reason = '', quarantined_at = NULL, updated_at = ?
			WHERE id = ? AND article_id = ? AND status IN ('sent_unconfirmed', 'quarantined')
			`, sqliteTime(now), reason, sqliteTime(now), id, articleID)
	case publishing.RepairDiscard:
		result, err = executor(ctx, r.db).ExecContext(ctx, `
			UPDATE publication_outbox SET status = 'dead', last_error = ?, updated_at = ?
			WHERE id = ? AND article_id = ? AND status IN ('sent_unconfirmed', 'quarantined')
			`, reason, sqliteTime(now), id, articleID)
	default:
		return fmt.Errorf("sqlite: repair outbox item %d: unrecognized action %q", id, action)
	}
	if err != nil {
		return fmt.Errorf("sqlite: repair outbox item %d: %w", id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: repair outbox item %d rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: repair outbox item %d: %w", id, appout.ErrNotRepairable)
	}
	return nil
}

// StatusCounts implements ports/out.OutboxRepository. OldestAge is the
// age, as of now, of the oldest row's created_at in that status — a
// coarse but dependency-free "how long has something been stuck"
// indicator for the CLI status command; it deliberately does not try to
// pick a per-status timestamp column (available_at/sent_at/quarantined_at
// mean different things per state), trading precision for one simple
// query an operator can also run by hand.
//
// The oldest-per-status lookup is a second, per-status query selecting
// the bare created_at column via ORDER BY/LIMIT 1, not a single
// GROUP BY ... MIN(created_at) query: this driver (modernc.org/sqlite)
// loses the column's TIMESTAMP decltype hint on an aggregate expression
// like MIN(...), so it comes back as a plain string the driver won't
// auto-convert to time.Time, while a bare column reference scans back
// correctly — see ClusterRepository.Stats's doc comment for the same
// finding.
//
// Both phases run inside one withTx call, exactly like ClaimBatch: without
// it, a status-mutating call (ClaimBatch/MarkRetryable/
// RecordSentUnconfirmed/...) from another goroutine could move every row
// of some status counted in phase one out of that status before phase
// two's per-status oldest-created_at lookup runs, making that lookup's
// QueryRowContext(...).Scan return sql.ErrNoRows and fail the whole call —
// an operator-facing CLI command that would then intermittently error
// under ordinary concurrent worker load. SQLite's deferred-transaction
// snapshot semantics make the two phases atomic against concurrent
// writers instead.
func (r *OutboxRepository) StatusCounts(ctx context.Context, now time.Time) (map[publishing.OutboxStatus]appout.StatusCount, error) {
	counts := make(map[publishing.OutboxStatus]appout.StatusCount)
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT status, COUNT(1), SUM(CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 1 ELSE 0 END)
			FROM publication_outbox GROUP BY status
		`, sqliteTime(now))
		if err != nil {
			return fmt.Errorf("sqlite: status counts: %w", err)
		}

		var statuses []string
		for rows.Next() {
			var status string
			var count, expiredCount int
			if err := rows.Scan(&status, &count, &expiredCount); err != nil {
				_ = rows.Close()
				return fmt.Errorf("sqlite: scan status count: %w", err)
			}
			counts[publishing.OutboxStatus(status)] = appout.StatusCount{Count: count, ExpiredCount: expiredCount}
			statuses = append(statuses, status)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlite: status counts: %w", err)
		}
		_ = rows.Close()

		for _, status := range statuses {
			var oldest sqliteNullTime
			if err := tx.QueryRowContext(ctx, `
					SELECT created_at FROM publication_outbox WHERE status = ? ORDER BY created_at ASC LIMIT 1
				`, status).Scan(&oldest); err != nil {
				return fmt.Errorf("sqlite: oldest outbox item for status %s: %w", status, err)
			}
			sc := counts[publishing.OutboxStatus(status)]
			if oldest.Valid {
				sc.OldestAge = now.Sub(oldest.Time)
			}
			counts[publishing.OutboxStatus(status)] = sc
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

// Defer implements ports/out.OutboxRepository.
func (r *OutboxRepository) Defer(ctx context.Context, id int64, availableAt time.Time, now time.Time) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE publication_outbox
		SET status = 'pending', available_at = ?, claimed_at = NULL, claimed_by = '', updated_at = ?
		WHERE id = ?
		`, sqliteTime(availableAt), sqliteTime(now), id)
	if err != nil {
		return fmt.Errorf("sqlite: defer outbox item %d: %w", id, err)
	}
	return nil
}

// RecordAttempt implements ports/out.OutboxRepository.
func (r *OutboxRepository) RecordAttempt(ctx context.Context, a appout.Attempt) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO publication_attempts (outbox_id, article_id, attempted_at, outcome, error_message, external_message_id, duration_ms)
		VALUES (?,?,?,?,?,?,?)
		`, a.OutboxID, a.ArticleID, sqliteTime(a.AttemptedAt), string(a.Outcome), a.ErrorMessage, a.ExternalMessageID, a.Duration.Milliseconds())
	if err != nil {
		return fmt.Errorf("sqlite: record attempt for outbox item %d: %w", a.OutboxID, err)
	}
	return nil
}

// DeleteOldAttempts implements ports/out.OutboxRepository.
func (r *OutboxRepository) DeleteOldAttempts(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := executor(ctx, r.db).ExecContext(ctx, `DELETE FROM publication_attempts WHERE attempted_at < ?`, sqliteTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("sqlite: delete attempts older than %s: %w", cutoff, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	return int(n), nil
}

// CountSucceededSince implements ports/out.OutboxRepository.
func (r *OutboxRepository) CountSucceededSince(ctx context.Context, since time.Time) (int, error) {
	var count int
	if err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT COUNT(1) FROM publication_attempts WHERE outcome = 'succeeded' AND attempted_at >= ?
	`, sqliteTime(since)).Scan(&count); err != nil {
		return 0, fmt.Errorf("sqlite: count succeeded attempts since %s: %w", since, err)
	}
	return count, nil
}

func (r *OutboxRepository) CountSucceededSinceLane(ctx context.Context, lane publishing.PublicationLane, since time.Time) (int, error) {
	var count int
	err := executor(ctx, r.db).QueryRowContext(ctx, `SELECT COUNT(*) FROM publication_attempts pa JOIN publication_outbox po ON po.id=pa.outbox_id WHERE pa.outcome='succeeded' AND pa.attempted_at >= ? AND po.lane=?`, sqliteTime(since), lane.Normalized()).Scan(&count)
	return count, err
}

// CountSucceededByPrimaryEntitySince implements ports/out.OutboxRepository.
func (r *OutboxRepository) CountSucceededByPrimaryEntitySince(ctx context.Context, entity string, since time.Time) (int, error) {
	var count int
	if err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM publication_attempts pa
		JOIN articles a ON a.id = pa.article_id
		WHERE pa.outcome = 'succeeded' AND pa.attempted_at >= ? AND a.primary_entity = ?
	`, sqliteTime(since), entity).Scan(&count); err != nil {
		return 0, fmt.Errorf("sqlite: count succeeded attempts for entity %q since %s: %w", entity, since, err)
	}
	return count, nil
}

func (r *OutboxRepository) CountSucceededByPrimaryEntitySinceLane(ctx context.Context, lane publishing.PublicationLane, entity string, since time.Time) (int, error) {
	var count int
	err := executor(ctx, r.db).QueryRowContext(ctx, `SELECT COUNT(*) FROM publication_attempts pa JOIN publication_outbox po ON po.id=pa.outbox_id JOIN articles a ON a.id=pa.article_id WHERE pa.outcome='succeeded' AND pa.attempted_at >= ? AND po.lane=? AND a.primary_entity=?`, sqliteTime(since), lane.Normalized(), entity).Scan(&count)
	return count, err
}
