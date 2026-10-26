package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// SaveRepresentation implements ports/out.ClusterRepository. It is a single
// INSERT OR REPLACE against event_representations.article_id (the table's
// primary key), so a second call for the same article replaces rather than
// duplicates the row.
func (r *ClusterRepository) SaveRepresentation(ctx context.Context, rep eventsignals.Representation, rulesVersion string, now time.Time) error {
	payload, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("sqlite: marshal representation for %s: %w", rep.ArticleID, err)
	}

	_, err = executor(ctx, r.db).ExecContext(
		ctx, `
		INSERT OR REPLACE INTO event_representations (
			article_id, rules_version, event_type, primary_entity, primary_action,
			event_date_window, representation_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rep.ArticleID, rulesVersion, string(rep.EventType), rep.PrimaryEntity, string(rep.Action),
		int64(rep.EventWindow), string(payload), sqliteTime(now),
	)
	if err != nil {
		return fmt.Errorf("sqlite: save representation for %s: %w", rep.ArticleID, err)
	}
	return nil
}

// SaveEventKeys implements ports/out.ClusterRepository. Each key's strength
// is its index in keys (0 = strongest, per eventsignals.Representation.
// ApproximateKeys' own strongest-to-weakest ordering). Duplicate
// (article_id, event_key) pairs are ignored, mirroring
// SimilarityIndexRepository.IndexTerms' idempotency contract; like
// IndexTerms, this loops per-key through executor(ctx, r.db) rather than
// wrapping every insert in a local transaction, since all inserts target
// this one table.
func (r *ClusterRepository) SaveEventKeys(ctx context.Context, articleID, clusterID string, keys []string, now time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	ex := executor(ctx, r.db)
	for strength, key := range keys {
		if _, err := ex.ExecContext(ctx, `
			INSERT OR IGNORE INTO event_keys (event_key, article_id, cluster_id, strength) VALUES (?, ?, ?, ?)
		`, key, articleID, clusterID, strength); err != nil {
			return fmt.Errorf("sqlite: save event key %q for %s: %w", key, articleID, err)
		}
	}
	return nil
}

// maxCandidatesPerCluster caps how many members of one cluster may enter the
// candidate list. Without it a cluster with many members matching a shared key
// (the team's generic key is shared by hundreds of articles) fills the whole
// limit and older clusters of the same story never reach clustering.Compare.
const maxCandidatesPerCluster = 3

// FindCandidatesByEventKeys implements ports/out.ClusterRepository. It
// filters entirely in SQL — the event_key IN (...) index lookup, a
// published_at time window, excludeArticleID, and LIMIT — before any
// clustering.Compare scoring happens in the caller; it never fetches more
// rows than the bound allows, mirroring
// SimilarityIndexRepository.FindCandidates' discipline exactly.
func (r *ClusterRepository) FindCandidatesByEventKeys(ctx context.Context, keys []string, windowStart time.Time, excludeArticleID string, limit int) ([]clustering.Candidate, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(keys))
	args := make([]any, 0, len(keys)+4)
	for i, k := range keys {
		placeholders[i] = "?"
		args = append(args, k)
	}

	excludeClause := ""
	if excludeArticleID != "" {
		excludeClause = "AND ek.article_id != ?"
	}

	query := fmt.Sprintf(`
		WITH per_article AS (
			SELECT ek.article_id, ek.cluster_id, er.representation_json, MIN(ek.strength) AS best_strength, a.published_at AS published_at
			FROM event_keys ek
			JOIN articles a ON a.id = ek.article_id
			JOIN event_representations er ON er.article_id = ek.article_id
			WHERE ek.event_key IN (%s)
			  AND a.published_at >= ?
			  %s
			GROUP BY ek.article_id
		), ranked AS (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY cluster_id ORDER BY best_strength ASC, published_at DESC) AS rn
			FROM per_article
		)
		SELECT article_id, cluster_id, representation_json, best_strength
		FROM ranked
		WHERE rn <= ?
		ORDER BY best_strength ASC, published_at DESC
		LIMIT ?
	`, strings.Join(placeholders, ","), excludeClause)

	args = append(args, sqliteTime(windowStart))
	if excludeArticleID != "" {
		args = append(args, excludeArticleID)
	}
	args = append(args, maxCandidatesPerCluster, limit)

	rows, err := executor(ctx, r.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: find candidates by event keys: %w", err)
	}
	defer rows.Close()

	var candidates []clustering.Candidate
	for rows.Next() {
		var articleID, clusterID, repJSON string
		var bestStrength int
		if err := rows.Scan(&articleID, &clusterID, &repJSON, &bestStrength); err != nil {
			return nil, fmt.Errorf("sqlite: scan candidate: %w", err)
		}
		var rep eventsignals.Representation
		if err := json.Unmarshal([]byte(repJSON), &rep); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal representation for candidate %s: %w", articleID, err)
		}
		candidates = append(candidates, clustering.Candidate{
			ArticleID:      articleID,
			ClusterID:      clusterID,
			Representation: rep,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate candidates: %w", err)
	}
	return candidates, nil
}

// SaveDecision implements ports/out.ClusterRepository.
func (r *ClusterRepository) SaveDecision(ctx context.Context, articleID, chosenClusterID string, d clustering.Decision) error {
	alternatives := d.Alternatives
	if alternatives == nil {
		alternatives = []clustering.Alternative{}
	}
	alternativesJSON, err := json.Marshal(alternatives)
	if err != nil {
		return fmt.Errorf("sqlite: marshal alternatives for %s: %w", articleID, err)
	}
	evidence := d.Evidence
	if evidence == nil {
		evidence = []clustering.Evidence{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("sqlite: marshal evidence for %s: %w", articleID, err)
	}

	var chosenClusterIDArg any
	if chosenClusterID != "" {
		chosenClusterIDArg = chosenClusterID
	}

	_, err = executor(ctx, r.db).ExecContext(
		ctx, `
		INSERT INTO event_clustering_decisions (
			article_id, chosen_cluster_id, action, member_role, score,
			alternatives_json, evidence_json, rules_version, decided_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		articleID, chosenClusterIDArg, string(d.Action), string(d.MemberRole), d.Score,
		string(alternativesJSON), string(evidenceJSON), d.RulesVersion, sqliteTime(d.DecidedAt),
	)
	if err != nil {
		return fmt.Errorf("sqlite: save decision for %s: %w", articleID, err)
	}
	return nil
}

// UpdateRepresentative implements ports/out.ClusterRepository.
//
// Safe to call from inside an outer UnitOfWork.Do(...) closure: withTx is
// reentrant and joins the in-flight transaction rather than opening a
// second one.
func (r *ClusterRepository) UpdateRepresentative(ctx context.Context, clusterID, articleID string, score float64, evidence []clustering.Evidence, rulesVersion string, now time.Time) error {
	if evidence == nil {
		evidence = []clustering.Evidence{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("sqlite: marshal representative evidence for cluster %s: %w", clusterID, err)
	}

	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		var priorArticle, priorEvidence, priorRules string
		var priorScore sql.NullFloat64
		err := tx.QueryRowContext(ctx, `
			SELECT representative_article_id, representative_score, representative_evidence_json, representative_rules_version
			FROM event_clusters WHERE id = ?
		`, clusterID).Scan(&priorArticle, &priorScore, &priorEvidence, &priorRules)
		if err != nil {
			return fmt.Errorf("sqlite: read current representative for cluster %s: %w", clusterID, err)
		}

		// Only archive a prior representative that was itself already
		// scored (representative_score IS NOT NULL) — the very first
		// representative, set unscored by CreateCluster, has nothing
		// meaningful to archive (see migration 0008's doc comment).
		if priorScore.Valid && priorArticle != "" {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO event_cluster_representative_history (
					cluster_id, article_id, score, evidence_json, rules_version, switched_at
				) VALUES (?, ?, ?, ?, ?, ?)
				`, clusterID, priorArticle, priorScore.Float64, priorEvidence, priorRules, sqliteTime(now)); err != nil {
				return fmt.Errorf("sqlite: archive prior representative for cluster %s: %w", clusterID, err)
			}
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE event_clusters
			SET representative_article_id = ?, representative_score = ?, representative_evidence_json = ?, representative_rules_version = ?, last_updated_at = ?
			WHERE id = ?
			`, articleID, score, string(evidenceJSON), rulesVersion, sqliteTime(now), clusterID); err != nil {
			return fmt.Errorf("sqlite: update representative for cluster %s: %w", clusterID, err)
		}
		return nil
	})
}
