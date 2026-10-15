package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SourcePolicyRepository implements ports/out.SourcePolicyDecisionRepository
// against SQLite.
type SourcePolicyRepository struct {
	db *sql.DB
}

// NewSourcePolicyRepository builds a SourcePolicyRepository over an
// already-open, already-migrated database.
func NewSourcePolicyRepository(db *sql.DB) *SourcePolicyRepository {
	return &SourcePolicyRepository{db: db}
}

var _ out.SourcePolicyDecisionRepository = (*SourcePolicyRepository)(nil)

// boolToText/textToBool implement this schema's boolean-as-text convention
// (see migration 0016's doc comment, matching articles.is_trusted_source).
func boolToText(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func textToBool(s string) bool {
	return s == "1"
}

const sourcePolicyDecisionColumns = `
	id, article_id, source_id, authority, policy_version, cluster_id,
	dedup_match_type, member_role, novelty, decision_code,
	would_publish_standard, would_publish_special, mode, cooldown_outcome, explanation, decided_at`

// Save implements ports/out.SourcePolicyDecisionRepository.
func (r *SourcePolicyRepository) Save(ctx context.Context, rec out.SourcePolicyDecisionRecord) error {
	var clusterIDArg any
	if rec.ClusterID != "" {
		clusterIDArg = rec.ClusterID
	}
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO source_policy_decisions (
			id, article_id, source_id, authority, policy_version, cluster_id,
			dedup_match_type, member_role, novelty, decision_code,
			would_publish_standard, would_publish_special, mode, cooldown_outcome, explanation, decided_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.ArticleID, rec.SourceID, rec.Authority, rec.PolicyVersion, clusterIDArg,
		rec.DedupMatchType, rec.MemberRole, rec.Novelty, rec.DecisionCode,
		boolToText(rec.WouldPublishStandard), boolToText(rec.WouldPublishSpecial), rec.Mode, rec.CooldownOutcome, rec.Explanation, sqliteTime(rec.DecidedAt),
	)
	if err != nil {
		return fmt.Errorf("sqlite: save source policy decision %s: %w", rec.ID, err)
	}
	return nil
}

func scanSourcePolicyDecision(row interface {
	Scan(dest ...any) error
},
) (out.SourcePolicyDecisionRecord, error) {
	var rec out.SourcePolicyDecisionRecord
	var clusterID sql.NullString
	var decidedAt sqliteNullTime
	var wps, wpsp string
	if err := row.Scan(
		&rec.ID, &rec.ArticleID, &rec.SourceID, &rec.Authority, &rec.PolicyVersion, &clusterID,
		&rec.DedupMatchType, &rec.MemberRole, &rec.Novelty, &rec.DecisionCode,
		&wps, &wpsp, &rec.Mode, &rec.CooldownOutcome, &rec.Explanation, &decidedAt,
	); err != nil {
		return out.SourcePolicyDecisionRecord{}, err
	}
	rec.ClusterID = clusterID.String
	rec.WouldPublishStandard = textToBool(wps)
	rec.WouldPublishSpecial = textToBool(wpsp)
	if decidedAt.Valid {
		rec.DecidedAt = decidedAt.Time
	}
	return rec, nil
}

// ByArticle implements ports/out.SourcePolicyDecisionRepository.
func (r *SourcePolicyRepository) ByArticle(ctx context.Context, articleID string) ([]out.SourcePolicyDecisionRecord, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `SELECT `+sourcePolicyDecisionColumns+`
		FROM source_policy_decisions WHERE article_id = ? ORDER BY decided_at ASC`, articleID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query source policy decisions for %s: %w", articleID, err)
	}
	defer rows.Close()

	var recs []out.SourcePolicyDecisionRecord
	for rows.Next() {
		rec, err := scanSourcePolicyDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan source policy decision: %w", err)
		}
		recs = append(recs, rec)
	}
	return recs, rows.Err()
}

// ListByRange implements ports/out.SourcePolicyDecisionRepository.
func (r *SourcePolicyRepository) ListByRange(ctx context.Context, from, to time.Time, sourceID, mode string) ([]out.SourcePolicyDecisionRecord, error) {
	query := `SELECT ` + sourcePolicyDecisionColumns + ` FROM source_policy_decisions WHERE decided_at >= ? AND decided_at < ?`
	args := []any{from, to}
	if sourceID != "" {
		query += ` AND source_id = ?`
		args = append(args, sourceID)
	}
	if mode != "" {
		query += ` AND mode = ?`
		args = append(args, mode)
	}
	query += ` ORDER BY decided_at ASC`

	rows, err := executor(ctx, r.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list source policy decisions: %w", err)
	}
	defer rows.Close()

	var result []out.SourcePolicyDecisionRecord
	for rows.Next() {
		rec, err := scanSourcePolicyDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan source policy decision: %w", err)
		}
		result = append(result, rec)
	}
	return result, rows.Err()
}
