// adapters/outbound/sqliteeventruleproposal/repository.go

// Package sqliteeventruleproposal implements ports/eventruleproposalport.Repository
// against SQLite (table event_rule_proposals, migration 0045).
package sqliteeventruleproposal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/eventruleproposalport"
)

// Repository implements eventruleproposalport.Repository.
type Repository struct {
	db *sql.DB
}

// NewRepository builds a Repository over an already-open, migrated database.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

var _ eventruleproposalport.Repository = (*Repository)(nil)

const timeFormat = time.RFC3339Nano

const selectColumns = `id, kind, target_kind, target, phrase, type_name, description,
	evidence_json, status, prompt_version, created_at, decided_at`

// normalize returns p with phrase and type name trimmed and lowercased, so
// every consumer (impact, checks, loader) sees identical strings.
func normalize(p eventruleproposal.Proposal) eventruleproposal.Proposal {
	p.Phrase = strings.ToLower(strings.TrimSpace(p.Phrase))
	p.TypeName = strings.ToLower(strings.TrimSpace(p.TypeName))
	p.Target = strings.TrimSpace(p.Target)
	return p
}

// proposalID derives the stable ID from the normalized logical key.
func proposalID(p eventruleproposal.Proposal) string {
	key := strings.Join([]string{string(p.Kind), string(p.TargetKind), p.Target, p.Phrase, p.TypeName}, "|")
	sum := sha256.Sum256([]byte(key))
	return "evr_" + hex.EncodeToString(sum[:])[:12]
}

type scanner interface{ Scan(dest ...any) error }

func scan(s scanner) (eventruleproposal.Proposal, error) {
	var (
		p                       eventruleproposal.Proposal
		kind, targetKind, state string
		evidence                string
		created, decided        any
	)
	if err := s.Scan(&p.ID, &kind, &targetKind, &p.Target, &p.Phrase, &p.TypeName, &p.Description,
		&evidence, &state, &p.PromptVersion, &created, &decided); err != nil {
		return eventruleproposal.Proposal{}, err
	}
	p.Kind = eventruleproposal.Kind(kind)
	p.TargetKind = eventruleproposal.TargetKind(targetKind)
	p.Status = eventruleproposal.Status(state)
	if err := json.Unmarshal([]byte(evidence), &p.EvidenceIDs); err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: decode evidence of %q: %w", p.ID, err)
	}
	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return eventruleproposal.Proposal{}, err
	}
	if p.DecidedAt, err = parseTime(decided); err != nil {
		return eventruleproposal.Proposal{}, err
	}
	return p, nil
}

func parseTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return t.UTC(), nil
	case string:
		return parseTimeString(t)
	case []byte:
		return parseTimeString(string(t))
	default:
		return time.Time{}, fmt.Errorf("sqliteeventruleproposal: unsupported timestamp type %T", v)
	}
}

func parseTimeString(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqliteeventruleproposal: parse timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeFormat)
}

// mergeEvidence returns existing followed by the new IDs not already present.
func mergeEvidence(existing, extra []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(extra))
	out := make([]string, 0, len(existing)+len(extra))
	for _, list := range [][]string{existing, extra} {
		for _, id := range list {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// Upsert implements eventruleproposalport.Repository.
func (r *Repository) Upsert(ctx context.Context, p eventruleproposal.Proposal) (eventruleproposal.Proposal, error) {
	p = normalize(p)
	p.ID = proposalID(p)
	if p.EvidenceIDs == nil {
		p.EvidenceIDs = []string{}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scan(tx.QueryRowContext(ctx,
		`SELECT `+selectColumns+` FROM event_rule_proposals
		 WHERE kind = ? AND target_kind = ? AND target = ? AND lower(phrase) = ? AND lower(type_name) = ?`,
		string(p.Kind), string(p.TargetKind), p.Target, p.Phrase, p.TypeName))
	switch {
	case err == nil:
		if existing.Status != eventruleproposal.StatusPending {
			return existing, tx.Commit()
		}
		existing.EvidenceIDs = mergeEvidence(existing.EvidenceIDs, p.EvidenceIDs)
		blob, merr := json.Marshal(existing.EvidenceIDs)
		if merr != nil {
			return eventruleproposal.Proposal{}, merr
		}
		if _, err := tx.ExecContext(ctx, `UPDATE event_rule_proposals SET evidence_json = ? WHERE id = ?`,
			string(blob), existing.ID); err != nil {
			return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: merge evidence: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: commit: %w", err)
		}
		return existing, nil
	case errors.Is(err, sql.ErrNoRows):
		p.EvidenceIDs = mergeEvidence(nil, p.EvidenceIDs)
		blob, merr := json.Marshal(p.EvidenceIDs)
		if merr != nil {
			return eventruleproposal.Proposal{}, merr
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO event_rule_proposals (`+selectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, string(p.Kind), string(p.TargetKind), p.Target, p.Phrase, p.TypeName, p.Description,
			string(blob), string(p.Status), p.PromptVersion, p.CreatedAt.UTC().Format(timeFormat), nullableTime(p.DecidedAt)); err != nil {
			return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: insert: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: commit: %w", err)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		return p, nil
	default:
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: lookup: %w", err)
	}
}

// ListByStatus implements eventruleproposalport.Repository.
func (r *Repository) ListByStatus(ctx context.Context, s eventruleproposal.Status) ([]eventruleproposal.Proposal, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+selectColumns+` FROM event_rule_proposals WHERE status = ? ORDER BY created_at, id`, string(s))
	if err != nil {
		return nil, fmt.Errorf("sqliteeventruleproposal: list: %w", err)
	}
	defer rows.Close()
	var out []eventruleproposal.Proposal
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqliteeventruleproposal: list: %w", err)
	}
	return out, nil
}

// Get implements eventruleproposalport.Repository.
func (r *Repository) Get(ctx context.Context, id string) (eventruleproposal.Proposal, error) {
	p, err := scan(r.db.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM event_rule_proposals WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: proposal %q: %w", id, err)
		}
		return eventruleproposal.Proposal{}, err
	}
	return p, nil
}

// Save implements eventruleproposalport.Repository.
func (r *Repository) Save(ctx context.Context, p eventruleproposal.Proposal) error {
	res, err := r.db.ExecContext(ctx, `UPDATE event_rule_proposals SET status = ?, decided_at = ? WHERE id = ?`,
		string(p.Status), nullableTime(p.DecidedAt), p.ID)
	if err != nil {
		return fmt.Errorf("sqliteeventruleproposal: save: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("sqliteeventruleproposal: proposal %q: %w", p.ID, sql.ErrNoRows)
	}
	return nil
}
