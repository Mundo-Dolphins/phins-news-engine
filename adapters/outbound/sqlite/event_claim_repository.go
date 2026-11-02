package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// EventClaimRepository implements ports/out.EventClaimRepository against
// SQLite.
type EventClaimRepository struct {
	db *sql.DB
}

// NewEventClaimRepository builds an EventClaimRepository over an
// already-open, already-migrated database.
func NewEventClaimRepository(db *sql.DB) *EventClaimRepository {
	return &EventClaimRepository{db: db}
}

var _ appout.EventClaimRepository = (*EventClaimRepository)(nil)

// TryClaim implements ports/out.EventClaimRepository. The INSERT's
// RowsAffected is the only signal of who won: UNIQUE (handler, claim_key)
// plus ON CONFLICT DO NOTHING makes the loser's insert a no-op.
func (r *EventClaimRepository) TryClaim(ctx context.Context, handler, key, articleID string, data json.RawMessage, now time.Time) (bool, error) {
	if handler == "" || key == "" {
		return false, errors.New("sqlite: claim event: handler and key are required")
	}
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if !json.Valid(data) {
		return false, fmt.Errorf("sqlite: claim event %s/%s: data is not valid JSON", handler, key)
	}
	res, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO event_claims (handler, claim_key, state, article_id, data, created_at, updated_at)
		VALUES (?, ?, 'claimed', ?, ?, ?, ?)
		ON CONFLICT(handler, claim_key) DO NOTHING
	`, handler, key, articleID, string(data), sqliteTime(now), sqliteTime(now))
	if err != nil {
		return false, fmt.Errorf("sqlite: claim event %s/%s: %w", handler, key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	return n == 1, nil
}

// MarkPublished implements ports/out.EventClaimRepository.
func (r *EventClaimRepository) MarkPublished(ctx context.Context, handler, key string, now time.Time) error {
	return r.setState(ctx, handler, key, appout.EventClaimPublished, now)
}

// MarkFailed implements ports/out.EventClaimRepository.
func (r *EventClaimRepository) MarkFailed(ctx context.Context, handler, key string, now time.Time) error {
	return r.setState(ctx, handler, key, appout.EventClaimFailed, now)
}

func (r *EventClaimRepository) setState(ctx context.Context, handler, key string, state appout.EventClaimState, now time.Time) error {
	res, err := executor(ctx, r.db).ExecContext(ctx,
		`UPDATE event_claims SET state = ?, updated_at = ? WHERE handler = ? AND claim_key = ?`,
		string(state), sqliteTime(now), handler, key)
	if err != nil {
		return fmt.Errorf("sqlite: mark event claim %s/%s %s: %w", handler, key, state, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: mark event claim %s/%s %s: %w", handler, key, state, appout.ErrEventClaimNotFound)
	}
	return nil
}

// Release implements ports/out.EventClaimRepository. The state check is in
// the DELETE itself, so a claim that turns published between a read and the
// delete is never removed.
func (r *EventClaimRepository) Release(ctx context.Context, handler, key string) (bool, error) {
	ex := executor(ctx, r.db)
	res, err := ex.ExecContext(ctx,
		`DELETE FROM event_claims WHERE handler = ? AND claim_key = ? AND state <> 'published'`,
		handler, key)
	if err != nil {
		return false, fmt.Errorf("sqlite: release event claim %s/%s: %w", handler, key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 1 {
		return true, nil
	}
	var state string
	err = ex.QueryRowContext(ctx,
		`SELECT state FROM event_claims WHERE handler = ? AND claim_key = ?`, handler, key).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sqlite: release event claim %s/%s: %w", handler, key, err)
	}
	return false, fmt.Errorf("sqlite: release event claim %s/%s: %w", handler, key, appout.ErrEventClaimPublished)
}

const eventClaimColumns = `id, handler, claim_key, state, article_id, data, created_at, updated_at`

func scanEventClaim(s rowScanner) (appout.EventClaim, error) {
	var (
		c         appout.EventClaim
		state     string
		data      string
		createdAt sqliteNullTime
		updatedAt sqliteNullTime
	)
	if err := s.Scan(&c.ID, &c.Handler, &c.Key, &state, &c.ArticleID, &data, &createdAt, &updatedAt); err != nil {
		return appout.EventClaim{}, err
	}
	c.State = appout.EventClaimState(state)
	c.Data = json.RawMessage(data)
	c.CreatedAt, c.UpdatedAt = createdAt.Time, updatedAt.Time
	return c, nil
}

// Get implements ports/out.EventClaimRepository.
func (r *EventClaimRepository) Get(ctx context.Context, handler, key string) (appout.EventClaim, bool, error) {
	c, err := scanEventClaim(executor(ctx, r.db).QueryRowContext(ctx,
		`SELECT `+eventClaimColumns+` FROM event_claims WHERE handler = ? AND claim_key = ?`, handler, key))
	if errors.Is(err, sql.ErrNoRows) {
		return appout.EventClaim{}, false, nil
	}
	if err != nil {
		return appout.EventClaim{}, false, fmt.Errorf("sqlite: get event claim %s/%s: %w", handler, key, err)
	}
	return c, true, nil
}

// List implements ports/out.EventClaimRepository.
func (r *EventClaimRepository) List(ctx context.Context, f appout.EventClaimFilter) ([]appout.EventClaim, error) {
	if f.State != "" && !f.State.Valid() {
		return nil, fmt.Errorf("sqlite: list event claims: unknown state %q", f.State)
	}
	query := `SELECT ` + eventClaimColumns + ` FROM event_claims WHERE (? = '' OR handler = ?) AND (? = '' OR state = ?) AND (? = '' OR article_id = ?) ORDER BY id DESC`
	args := []any{f.Handler, f.Handler, string(f.State), string(f.State), f.ArticleID, f.ArticleID}
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := executor(ctx, r.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list event claims: %w", err)
	}
	defer rows.Close()
	var claims []appout.EventClaim
	for rows.Next() {
		c, err := scanEventClaim(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan event claim: %w", err)
		}
		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list event claims: %w", err)
	}
	return claims, nil
}

// ReconcileWithOutbox implements ports/out.EventClaimRepository. "Finished"
// means delivered or dead; any other outbox status keeps the claim open.
func (r *EventClaimRepository) ReconcileWithOutbox(ctx context.Context, now time.Time) (int, int, error) {
	var published, failed int
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		const finished = `
			state = 'claimed' AND article_id <> ''
			AND EXISTS (SELECT 1 FROM publication_outbox o WHERE o.article_id = event_claims.article_id)
			AND NOT EXISTS (SELECT 1 FROM publication_outbox o
				WHERE o.article_id = event_claims.article_id AND o.status NOT IN ('delivered','dead'))`
		res, err := tx.ExecContext(ctx, `
			UPDATE event_claims SET state = 'failed', updated_at = ?
			WHERE `+finished+`
			AND EXISTS (SELECT 1 FROM publication_outbox o WHERE o.article_id = event_claims.article_id AND o.status = 'dead')`,
			sqliteTime(now))
		if err != nil {
			return fmt.Errorf("sqlite: reconcile failed event claims: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("sqlite: rows affected: %w", err)
		}
		failed = int(n)
		res, err = tx.ExecContext(ctx, `
			UPDATE event_claims SET state = 'published', updated_at = ?
			WHERE `+finished, sqliteTime(now))
		if err != nil {
			return fmt.Errorf("sqlite: reconcile published event claims: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("sqlite: rows affected: %w", err)
		}
		published = int(n)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return published, failed, nil
}
