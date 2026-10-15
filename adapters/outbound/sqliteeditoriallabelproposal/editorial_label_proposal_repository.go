// Package sqliteeditoriallabelproposal implements editorial label
// proposal storage for offline tools only.
package sqliteeditoriallabelproposal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
)

// EditorialLabelProposalRepository implements
// editoriallabelproposalport.EditorialLabelProposalRepository against
// SQLite.
type EditorialLabelProposalRepository struct {
	db *sql.DB
}

// NewEditorialLabelProposalRepository builds a repository over an
// already-open, already-migrated database.
func NewEditorialLabelProposalRepository(db *sql.DB) *EditorialLabelProposalRepository {
	return &EditorialLabelProposalRepository{db: db}
}

var _ editoriallabelproposalport.EditorialLabelProposalRepository = (*EditorialLabelProposalRepository)(nil)

const sqliteTimeFormat = time.RFC3339Nano

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
		return fmt.Errorf("sqliteeditoriallabelproposal: unsupported timestamp type %T", value)
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
	return fmt.Errorf("sqliteeditoriallabelproposal: parse timestamp %q", value)
}

// Save implements editoriallabelproposalport.EditorialLabelProposalRepository.
func (r *EditorialLabelProposalRepository) Save(ctx context.Context, p editoriallabelproposal.Proposal) (string, error) {
	id := p.ID
	if id == "" {
		id = uuid.NewString()
	}
	status := p.Status
	if status == "" {
		status = editoriallabelproposal.StatusPending
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO editorial_label_proposals (
			id, article_id, label, confidence, reason, model, prompt_version, policy_version, status, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)
	`, id, p.ArticleID, p.Label, p.Confidence, p.Reason, p.Model, p.PromptVersion, p.PolicyVersion, string(status), sqliteTime(p.CreatedAt))
	if err != nil {
		if isPendingProposalConflict(err) {
			return "", fmt.Errorf("sqliteeditoriallabelproposal: save proposal for %s: %w", p.ArticleID, editoriallabelproposal.ErrPendingProposalExists)
		}
		return "", fmt.Errorf("sqliteeditoriallabelproposal: save proposal for %s: %w", p.ArticleID, err)
	}
	return id, nil
}

// isPendingProposalConflict reports whether err is the unique-constraint
// violation on idx_editorial_label_proposals_pending_identity. Detected
// by message text (modernc.org/sqlite does not export a typed
// constraint-kind error), same approach implied by this codebase's
// existing UNIQUE-constraint test assertions (see migrate_test.go).
func isPendingProposalConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint")
}

// GetLatestByArticle implements
// editoriallabelproposalport.EditorialLabelProposalRepository.
func (r *EditorialLabelProposalRepository) GetLatestByArticle(ctx context.Context, articleID string) (editoriallabelproposal.Proposal, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, article_id, label, confidence, reason, model, prompt_version, policy_version, status, created_at, decided_at
		FROM editorial_label_proposals
		WHERE article_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, articleID)
	p, err := scanProposal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return editoriallabelproposal.Proposal{}, false, nil
	}
	if err != nil {
		return editoriallabelproposal.Proposal{}, false, fmt.Errorf("sqliteeditoriallabelproposal: get latest proposal for %s: %w", articleID, err)
	}
	return p, true, nil
}

// ListLatestPerArticle implements
// editoriallabelproposalport.EditorialLabelProposalRepository.
func (r *EditorialLabelProposalRepository) ListLatestPerArticle(ctx context.Context) ([]editoriallabelproposal.Proposal, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, p.article_id, p.label, p.confidence, p.reason, p.model, p.prompt_version, p.policy_version, p.status, p.created_at, p.decided_at
		FROM editorial_label_proposals p
		WHERE p.id = (
			SELECT p2.id FROM editorial_label_proposals p2
			WHERE p2.article_id = p.article_id
			ORDER BY p2.created_at DESC, p2.id DESC
			LIMIT 1
		)
		ORDER BY p.article_id
	`)
	if err != nil {
		return nil, fmt.Errorf("sqliteeditoriallabelproposal: list latest proposal per article: %w", err)
	}
	defer rows.Close()
	var out []editoriallabelproposal.Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("sqliteeditoriallabelproposal: scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListByStatus implements
// editoriallabelproposalport.EditorialLabelProposalRepository.
func (r *EditorialLabelProposalRepository) ListByStatus(ctx context.Context, status editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, article_id, label, confidence, reason, model, prompt_version, policy_version, status, created_at, decided_at
		FROM editorial_label_proposals
		WHERE status = ?
		ORDER BY created_at ASC, id ASC
	`, string(status))
	if err != nil {
		return nil, fmt.Errorf("sqliteeditoriallabelproposal: list proposals by status %s: %w", status, err)
	}
	defer rows.Close()
	var out []editoriallabelproposal.Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("sqliteeditoriallabelproposal: scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProposal(row rowScanner) (editoriallabelproposal.Proposal, error) {
	var p editoriallabelproposal.Proposal
	var status string
	var createdAt, decidedAt nullTime
	if err := row.Scan(&p.ID, &p.ArticleID, &p.Label, &p.Confidence, &p.Reason, &p.Model,
		&p.PromptVersion, &p.PolicyVersion, &status, &createdAt, &decidedAt); err != nil {
		return editoriallabelproposal.Proposal{}, err
	}
	p.Status = editoriallabelproposal.Status(status)
	p.CreatedAt = createdAt.Time
	p.DecidedAt = decidedAt.Time
	return p, nil
}

// UpdateStatus implements
// editoriallabelproposalport.EditorialLabelProposalRepository.
func (r *EditorialLabelProposalRepository) UpdateStatus(ctx context.Context, id string, status editoriallabelproposal.Status, decidedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE editorial_label_proposals SET status = ?, decided_at = ? WHERE id = ?
	`, string(status), sqliteTime(decidedAt), id)
	if err != nil {
		return fmt.Errorf("sqliteeditoriallabelproposal: update proposal status %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqliteeditoriallabelproposal: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sqliteeditoriallabelproposal: update proposal status %s: %w", id, editoriallabelproposal.ErrNotFound)
	}
	return nil
}

// FilterUnprocessed implements
// editoriallabelproposalport.EditorialLabelProposalRepository.
func (r *EditorialLabelProposalRepository) FilterUnprocessed(ctx context.Context, promptVersion string, articleIDs []string) ([]string, error) {
	if len(articleIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(articleIDs))
	args := make([]any, 0, len(articleIDs)+1)
	args = append(args, promptVersion)
	for i, id := range articleIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT article_id FROM editorial_label_analysis_processed_articles
		WHERE prompt_version = ? AND article_id IN (%s)
	`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("sqliteeditoriallabelproposal: filter unprocessed articles: %w", err)
	}
	defer rows.Close()
	processed := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqliteeditoriallabelproposal: scan processed article id: %w", err)
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

// MarkArticlesProcessed implements
// editoriallabelproposalport.EditorialLabelProposalRepository.
func (r *EditorialLabelProposalRepository) MarkArticlesProcessed(ctx context.Context, promptVersion string, articleIDs []string, processedAt time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqliteeditoriallabelproposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range articleIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO editorial_label_analysis_processed_articles (article_id, prompt_version, processed_at)
			VALUES (?, ?, ?)
		`, id, promptVersion, sqliteTime(processedAt)); err != nil {
			return fmt.Errorf("sqliteeditoriallabelproposal: mark article %s processed: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqliteeditoriallabelproposal: commit transaction: %w", err)
	}
	return nil
}
