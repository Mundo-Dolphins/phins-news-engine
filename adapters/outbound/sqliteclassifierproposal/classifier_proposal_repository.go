// Package sqliteclassifierproposal implements classifier proposal storage for
// offline tools only.
package sqliteclassifierproposal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/classifierproposalport"
)

type ClassifierProposalRepository struct {
	db *sql.DB
}

func NewClassifierProposalRepository(db *sql.DB) *ClassifierProposalRepository {
	return &ClassifierProposalRepository{db: db}
}

var _ classifierproposalport.ClassifierProposalRepository = (*ClassifierProposalRepository)(nil)

const sqliteTimeFormat = time.RFC3339Nano

func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqliteclassifierproposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqliteclassifierproposal: commit transaction: %w", err)
	}
	return nil
}

func sqliteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

type nullTime struct {
	Time  time.Time
	Valid bool
}

func (t *nullTime) Scan(value any) error {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case time.Time:
		t.Time = v.UTC()
		t.Valid = true
		return nil
	case string:
		return t.scanString(v)
	case []byte:
		return t.scanString(string(v))
	default:
		return fmt.Errorf("sqliteclassifierproposal: unsupported timestamp type %T", value)
	}
}

func (t *nullTime) scanString(value string) error {
	s := strings.TrimSpace(value)
	if s == "" {
		return nil
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed.UTC()
			t.Valid = true
			return nil
		}
	}
	return fmt.Errorf("sqliteclassifierproposal: parse timestamp %q", value)
}

func (r *ClassifierProposalRepository) Upsert(ctx context.Context, p classifierproposal.Proposal) (string, error) {
	var finalID string
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		var existingID string
		var confidence float64
		var metadataJSON string
		var firstSeen, lastSeen nullTime
		row := tx.QueryRowContext(ctx, `
			SELECT id, confidence, metadata_json, first_seen, last_seen
			FROM classifier_dictionary_proposals
			WHERE normalized_value = ? AND kind = ? AND category = ? AND operation = ? AND status = 'pending'
		`, p.NormalizedValue, string(p.Kind), p.Category, p.Operation)
		err := row.Scan(&existingID, &confidence, &metadataJSON, &firstSeen, &lastSeen)
		switch {
		case err == sql.ErrNoRows:
			finalID = p.ID
			if finalID == "" {
				finalID = uuid.NewString()
			}
			metaJSON, err := json.Marshal(p.Metadata)
			if err != nil {
				return fmt.Errorf("marshal metadata: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO classifier_dictionary_proposals (
					id, kind, category, value, normalized_value, operation, target_entry,
					metadata_json, confidence, occurrences, status, first_seen, last_seen,
					analyzer_version, created_at
				) VALUES (?,?,?,?,?,?,?, ?,?,?,?,?,?, ?,?)
			`, finalID, string(p.Kind), p.Category, p.Value, p.NormalizedValue, p.Operation, p.TargetEntry,
				string(metaJSON), p.Confidence, p.Occurrences, string(classifierproposal.StatusPending),
				sqliteTime(p.FirstSeen), sqliteTime(p.LastSeen), p.AnalyzerVersion, sqliteTime(p.CreatedAt)); err != nil {
				return fmt.Errorf("insert classifier proposal: %w", err)
			}
			return r.writeSources(ctx, tx, finalID, p.Sources)
		case err != nil:
			return fmt.Errorf("select pending classifier proposal: %w", err)
		default:
			finalID = existingID
			mergedMeta := map[string]string{}
			_ = json.Unmarshal([]byte(metadataJSON), &mergedMeta)
			for k, v := range p.Metadata {
				if _, ok := mergedMeta[k]; !ok && v != "" {
					mergedMeta[k] = v
				}
			}
			metaJSON, err := json.Marshal(mergedMeta)
			if err != nil {
				return fmt.Errorf("marshal merged metadata: %w", err)
			}
			newConfidence := confidence
			if p.Confidence > newConfidence {
				newConfidence = p.Confidence
			}
			newFirstSeen := firstSeen.Time
			if !firstSeen.Valid || p.FirstSeen.Before(newFirstSeen) {
				newFirstSeen = p.FirstSeen
			}
			newLastSeen := lastSeen.Time
			if !lastSeen.Valid || p.LastSeen.After(newLastSeen) {
				newLastSeen = p.LastSeen
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE classifier_dictionary_proposals
				SET occurrences = occurrences + ?, confidence = ?, metadata_json = ?, first_seen = ?, last_seen = ?
				WHERE id = ?
			`, p.Occurrences, newConfidence, string(metaJSON), sqliteTime(newFirstSeen), sqliteTime(newLastSeen), existingID); err != nil {
				return fmt.Errorf("update classifier proposal: %w", err)
			}
			return r.writeSources(ctx, tx, existingID, p.Sources)
		}
	})
	if err != nil {
		return "", fmt.Errorf("sqliteclassifierproposal: upsert classifier proposal: %w", err)
	}
	return finalID, nil
}

func (r *ClassifierProposalRepository) writeSources(ctx context.Context, tx *sql.Tx, proposalID string, sources []classifierproposal.Source) error {
	for _, s := range sources {
		if s.ArticleID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO classifier_dictionary_proposal_sources (proposal_id, article_id, article_title)
			VALUES (?, ?, ?)
		`, proposalID, s.ArticleID, s.ArticleTitle); err != nil {
			return fmt.Errorf("insert proposal source: %w", err)
		}
	}
	return nil
}

func (r *ClassifierProposalRepository) Get(ctx context.Context, id string) (classifierproposal.Proposal, error) {
	p, err := r.scanOne(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(classifierproposal.ErrNotFound, err)
		}
		return classifierproposal.Proposal{}, fmt.Errorf("sqliteclassifierproposal: get classifier proposal %s: %w", id, err)
	}
	return p, nil
}

func (r *ClassifierProposalRepository) scanOne(ctx context.Context, id string) (classifierproposal.Proposal, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, kind, category, value, normalized_value, operation, target_entry,
		       metadata_json, confidence, occurrences, status, first_seen, last_seen,
		       analyzer_version, created_at, decided_at
		FROM classifier_dictionary_proposals WHERE id = ?
	`, id)
	p, err := scanProposalRow(row)
	if err != nil {
		return classifierproposal.Proposal{}, err
	}
	sources, err := r.listSources(ctx, id)
	if err != nil {
		return classifierproposal.Proposal{}, err
	}
	p.Sources = sources
	return p, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProposalRow(row rowScanner) (classifierproposal.Proposal, error) {
	var p classifierproposal.Proposal
	var kind, status string
	var firstSeen, lastSeen, createdAt, decidedAt nullTime
	var metadataJSON string
	if err := row.Scan(&p.ID, &kind, &p.Category, &p.Value, &p.NormalizedValue, &p.Operation, &p.TargetEntry,
		&metadataJSON, &p.Confidence, &p.Occurrences, &status, &firstSeen, &lastSeen,
		&p.AnalyzerVersion, &createdAt, &decidedAt); err != nil {
		return classifierproposal.Proposal{}, err
	}
	p.Kind = classifierproposal.Kind(kind)
	p.Status = classifierproposal.Status(status)
	p.FirstSeen = firstSeen.Time
	p.LastSeen = lastSeen.Time
	p.CreatedAt = createdAt.Time
	p.DecidedAt = decidedAt.Time
	p.Metadata = map[string]string{}
	_ = json.Unmarshal([]byte(metadataJSON), &p.Metadata)
	return p, nil
}

func (r *ClassifierProposalRepository) listSources(ctx context.Context, id string) ([]classifierproposal.Source, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT article_id, article_title
		FROM classifier_dictionary_proposal_sources
		WHERE proposal_id = ?
		ORDER BY article_id
	`, id)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()
	var sources []classifierproposal.Source
	for rows.Next() {
		var s classifierproposal.Source
		if err := rows.Scan(&s.ArticleID, &s.ArticleTitle); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, s)
	}
	return sources, rows.Err()
}

func (r *ClassifierProposalRepository) List(ctx context.Context, kind, status string) ([]classifierproposal.Proposal, error) {
	query := `
		SELECT id, kind, category, value, normalized_value, operation, target_entry,
		       metadata_json, confidence, occurrences, status, first_seen, last_seen,
		       analyzer_version, created_at, decided_at
		FROM classifier_dictionary_proposals WHERE 1=1
	`
	var args []any
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqliteclassifierproposal: list classifier proposals: %w", err)
	}
	defer rows.Close()
	var proposals []classifierproposal.Proposal
	var ids []string
	for rows.Next() {
		p, err := scanProposalRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan classifier proposal: %w", err)
		}
		proposals = append(proposals, p)
		ids = append(ids, p.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqliteclassifierproposal: list classifier proposals: %w", err)
	}
	for i, id := range ids {
		sources, err := r.listSources(ctx, id)
		if err != nil {
			return nil, err
		}
		proposals[i].Sources = sources
	}
	return proposals, nil
}

func (r *ClassifierProposalRepository) UpdateStatus(ctx context.Context, id string, status classifierproposal.Status, decidedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE classifier_dictionary_proposals SET status = ?, decided_at = ? WHERE id = ?
	`, string(status), sqliteTime(decidedAt), id)
	if err != nil {
		return fmt.Errorf("sqliteclassifierproposal: update classifier proposal status %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqliteclassifierproposal: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sqliteclassifierproposal: update classifier proposal status %s: %w", id, classifierproposal.ErrNotFound)
	}
	return nil
}

func (r *ClassifierProposalRepository) FilterUnprocessed(ctx context.Context, analyzerVersion string, articleIDs []string) ([]string, error) {
	if len(articleIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(articleIDs))
	args := make([]any, 0, len(articleIDs)+1)
	args = append(args, analyzerVersion)
	for i, id := range articleIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT article_id FROM classifier_dictionary_analysis_processed_articles
		WHERE analyzer_version = ? AND article_id IN (%s)
	`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("sqliteclassifierproposal: filter unprocessed articles: %w", err)
	}
	defer rows.Close()
	processed := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan processed article id: %w", err)
		}
		processed[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var unprocessed []string
	for _, id := range articleIDs {
		if !processed[id] {
			unprocessed = append(unprocessed, id)
		}
	}
	return unprocessed, nil
}

func (r *ClassifierProposalRepository) MarkArticlesProcessed(ctx context.Context, analyzerVersion string, articleIDs []string, processedAt time.Time) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, id := range articleIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO classifier_dictionary_analysis_processed_articles (article_id, analyzer_version, processed_at)
				VALUES (?, ?, ?)
			`, id, analyzerVersion, sqliteTime(processedAt)); err != nil {
				return fmt.Errorf("mark article %s processed: %w", id, err)
			}
		}
		return nil
	})
}

func (r *ClassifierProposalRepository) ListUnknownAggregates(ctx context.Context, since time.Time, minOccurrences, limit int) ([]classifierproposalport.UnknownOccurrenceAggregate, error) {
	if minOccurrences <= 0 {
		minOccurrences = 2
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT u.candidate_text, u.normalized_text, COUNT(1), MIN(u.created_at), MAX(u.created_at),
		       COALESCE(MIN(u.article_id), ''), COALESCE(MIN(a.title), '')
		FROM unknown_entity_occurrences u
		LEFT JOIN articles a ON a.id = u.article_id
		WHERE u.created_at >= ?
		GROUP BY u.normalized_text
		HAVING COUNT(1) >= ?
		ORDER BY COUNT(1) DESC, u.normalized_text ASC
		LIMIT ?
	`, sqliteTime(since), minOccurrences, limit)
	if err != nil {
		return nil, fmt.Errorf("sqliteclassifierproposal: list unknown aggregates: %w", err)
	}
	defer rows.Close()
	var out []classifierproposalport.UnknownOccurrenceAggregate
	for rows.Next() {
		var a classifierproposalport.UnknownOccurrenceAggregate
		var firstSeen, lastSeen nullTime
		if err := rows.Scan(&a.Value, &a.Normalized, &a.Occurrences, &firstSeen, &lastSeen, &a.ArticleID, &a.ArticleTitle); err != nil {
			return nil, fmt.Errorf("scan unknown aggregate: %w", err)
		}
		a.FirstSeen = firstSeen.Time
		a.LastSeen = lastSeen.Time
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *ClassifierProposalRepository) Metrics(ctx context.Context, since time.Time) (classifierproposalport.Metrics, error) {
	var m classifierproposalport.Metrics
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT analyzer_version)
		FROM classifier_dictionary_analysis_processed_articles
		WHERE processed_at >= ?
	`, sqliteTime(since)).Scan(&m.AnalysisRunsTotal); err != nil {
		return m, fmt.Errorf("metrics analysis runs: %w", err)
	}
	if err := r.db.QueryRowContext(ctx, `
		SELECT
			COUNT(1),
			COALESCE(SUM(CASE WHEN status = 'approved' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'rejected' THEN 1 ELSE 0 END), 0)
		FROM classifier_dictionary_proposals
		WHERE created_at >= ?
	`, sqliteTime(since)).Scan(&m.ProposalsTotal, &m.ProposalsApprovedTotal, &m.ProposalsRejectedTotal); err != nil {
		return m, fmt.Errorf("metrics proposals: %w", err)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM unknown_entity_occurrences WHERE created_at >= ?`, sqliteTime(since)).Scan(&m.UnknownEntityCount); err != nil {
		return m, fmt.Errorf("metrics unknown entities: %w", err)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM articles WHERE fetched_at >= ?`, sqliteTime(since)).Scan(&m.EntityExtractionTotal); err != nil {
		return m, fmt.Errorf("metrics articles: %w", err)
	}
	m.EntityExtractionUnknownTotal = m.UnknownEntityCount
	return m, nil
}
