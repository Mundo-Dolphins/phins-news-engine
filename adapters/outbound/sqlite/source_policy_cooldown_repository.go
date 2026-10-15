// adapters/outbound/sqlite/source_policy_cooldown_repository.go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SourcePolicyCooldownRepository implements
// ports/out.SourcePolicyCooldownRepository against SQLite — the
// persistence-level guarantee that sourcepolicy.Policy.EventCooldown is
// consumed only by an effective publication or an actively-held
// reservation.
type SourcePolicyCooldownRepository struct {
	db *sql.DB
}

// NewSourcePolicyCooldownRepository builds a SourcePolicyCooldownRepository
// over an already-open, already-migrated database.
func NewSourcePolicyCooldownRepository(db *sql.DB) *SourcePolicyCooldownRepository {
	return &SourcePolicyCooldownRepository{db: db}
}

var _ out.SourcePolicyCooldownRepository = (*SourcePolicyCooldownRepository)(nil)

// Reserve implements ports/out.SourcePolicyCooldownRepository. The single
// upsert statement is the compare-and-swap: it inserts (no row existed),
// updates (existing row was 'released', or its window has naturally
// elapsed), or changes nothing at all (an active window is genuinely
// held) — RowsAffected distinguishes "acquired" from "lost the race",
// with no error raised for the latter.
func (r *SourcePolicyCooldownRepository) Reserve(ctx context.Context, res out.SourcePolicyCooldownReservation, cooldown time.Duration, now time.Time) (bool, error) {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO source_policy_cooldowns
			(source_id, cluster_id, state, article_id, decision_code, policy_version, reserved_at, cooldown_until)
		VALUES (?, ?, 'reserved', ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, cluster_id) DO UPDATE SET
			state = 'reserved', article_id = excluded.article_id, decision_code = excluded.decision_code,
			policy_version = excluded.policy_version, reserved_at = excluded.reserved_at,
			cooldown_until = excluded.cooldown_until, confirmed_at = NULL, released_at = NULL
		WHERE source_policy_cooldowns.state = 'released'
		   OR source_policy_cooldowns.cooldown_until <= excluded.reserved_at
		`, res.SourceID, res.ClusterID, res.ArticleID, res.DecisionCode, res.PolicyVersion, sqliteTime(now), sqliteTime(now.Add(cooldown)))
	if err != nil {
		return false, fmt.Errorf("sqlite: reserve source policy cooldown for %s/%s: %w", res.SourceID, res.ClusterID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: rows affected reserving cooldown for %s/%s: %w", res.SourceID, res.ClusterID, err)
	}
	return n > 0, nil
}

// ForceReserve implements ports/out.SourcePolicyCooldownRepository — the
// same upsert as Reserve but with no WHERE clause, so it always wins
// regardless of the existing row's state or cooldown_until.
func (r *SourcePolicyCooldownRepository) ForceReserve(ctx context.Context, res out.SourcePolicyCooldownReservation, cooldown time.Duration, now time.Time) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO source_policy_cooldowns
			(source_id, cluster_id, state, article_id, decision_code, policy_version, reserved_at, cooldown_until)
		VALUES (?, ?, 'reserved', ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, cluster_id) DO UPDATE SET
			state = 'reserved', article_id = excluded.article_id, decision_code = excluded.decision_code,
			policy_version = excluded.policy_version, reserved_at = excluded.reserved_at,
			cooldown_until = excluded.cooldown_until, confirmed_at = NULL, released_at = NULL
		`, res.SourceID, res.ClusterID, res.ArticleID, res.DecisionCode, res.PolicyVersion, sqliteTime(now), sqliteTime(now.Add(cooldown)))
	if err != nil {
		return fmt.Errorf("sqlite: force-reserve source policy cooldown for %s/%s: %w", res.SourceID, res.ClusterID, err)
	}
	return nil
}

// ActiveCooldownUntil implements ports/out.SourcePolicyCooldownRepository.
func (r *SourcePolicyCooldownRepository) ActiveCooldownUntil(ctx context.Context, sourceID, clusterID string) (time.Time, bool, error) {
	var until sqliteNullTime
	err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT cooldown_until FROM source_policy_cooldowns
		WHERE source_id = ? AND cluster_id = ? AND state IN ('reserved', 'confirmed')`, sourceID, clusterID).Scan(&until)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("sqlite: active cooldown for %s/%s: %w", sourceID, clusterID, err)
	}
	if !until.Valid {
		return time.Time{}, false, nil
	}
	return until.Time, true, nil
}

// Confirm implements ports/out.SourcePolicyCooldownRepository.
func (r *SourcePolicyCooldownRepository) Confirm(ctx context.Context, sourceID, clusterID, articleID string, outboxID int64, now time.Time) (bool, error) {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE source_policy_cooldowns SET state = 'confirmed', outbox_id = ?, confirmed_at = ?
		WHERE source_id = ? AND cluster_id = ? AND article_id = ?
		`, outboxID, sqliteTime(now), sourceID, clusterID, articleID)
	if err != nil {
		return false, fmt.Errorf("sqlite: confirm source policy cooldown for %s/%s: %w", sourceID, clusterID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: rows affected confirming cooldown for %s/%s: %w", sourceID, clusterID, err)
	}
	return n > 0, nil
}

// Release implements ports/out.SourcePolicyCooldownRepository.
func (r *SourcePolicyCooldownRepository) Release(ctx context.Context, sourceID, clusterID, articleID string, now time.Time) (bool, error) {
	result, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE source_policy_cooldowns SET state = 'released', released_at = ?
		WHERE source_id = ? AND cluster_id = ? AND article_id = ?
		`, sqliteTime(now), sourceID, clusterID, articleID)
	if err != nil {
		return false, fmt.Errorf("sqlite: release source policy cooldown for %s/%s: %w", sourceID, clusterID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: rows affected releasing cooldown for %s/%s: %w", sourceID, clusterID, err)
	}
	return n > 0, nil
}
