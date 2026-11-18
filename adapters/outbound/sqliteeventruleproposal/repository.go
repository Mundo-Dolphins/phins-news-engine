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
	evidence_json, status, prompt_version, created_at, decided_at, type_window, type_referential`

// normalize returns p with phrase and type name trimmed and lowercased, so
// every consumer (impact, checks, loader) sees identical strings. A type
// proposal's name is reduced to its slug.
func normalize(p eventruleproposal.Proposal) eventruleproposal.Proposal {
	p.Phrase = strings.ToLower(strings.TrimSpace(p.Phrase))
	p.TypeName = strings.ToLower(strings.TrimSpace(p.TypeName))
	if p.Kind == eventruleproposal.KindType {
		p.TypeName = eventruleproposal.Slug(p.TypeName)
	}
	p.Target = strings.TrimSpace(p.Target)
	if p.Kind == eventruleproposal.KindType && strings.TrimSpace(p.TypeWindow) == "" {
		p.TypeWindow = eventruleproposal.DefaultTypeWindow
	}
	return p
}

// proposalID derives the stable ID from the normalized logical key.
// A type proposal is keyed by its slug alone (kind and slug, no target kind
// or target), so renamed targets do not split it.
func proposalID(p eventruleproposal.Proposal) string {
	if p.Kind == eventruleproposal.KindType {
		p.TargetKind, p.Target, p.Phrase = "", "", ""
	}
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
		referential             int
	)
	if err := s.Scan(&p.ID, &kind, &targetKind, &p.Target, &p.Phrase, &p.TypeName, &p.Description,
		&evidence, &state, &p.PromptVersion, &created, &decided, &p.TypeWindow, &referential); err != nil {
		return eventruleproposal.Proposal{}, err
	}
	p.Kind = eventruleproposal.Kind(kind)
	p.TargetKind = eventruleproposal.TargetKind(targetKind)
	p.Status = eventruleproposal.Status(state)
	p.TypeReferential = referential != 0
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

	var row scanner
	if p.Kind == eventruleproposal.KindType {
		row = tx.QueryRowContext(ctx,
			`SELECT `+selectColumns+` FROM event_rule_proposals WHERE kind = 'type' AND type_name = ?`,
			p.TypeName)
	} else {
		row = tx.QueryRowContext(ctx,
			`SELECT `+selectColumns+` FROM event_rule_proposals
		 WHERE kind = ? AND target_kind = ? AND target = ? AND lower(phrase) = ? AND lower(type_name) = ?`,
			string(p.Kind), string(p.TargetKind), p.Target, p.Phrase, p.TypeName)
	}
	existing, err := scan(row)
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
		if existing.Target == "" && p.Target != "" {
			existing.Target = p.Target
		}
		if _, err := tx.ExecContext(ctx, `UPDATE event_rule_proposals SET evidence_json = ?, target = ? WHERE id = ?`,
			string(blob), existing.Target, existing.ID); err != nil {
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
		if err := insertProposal(ctx, tx, p, string(blob)); err != nil {
			return eventruleproposal.Proposal{}, err
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

// insertProposal inserts an already normalized proposal with its ID and
// JSON-encoded evidence set.
func insertProposal(ctx context.Context, tx *sql.Tx, p eventruleproposal.Proposal, evidenceJSON string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO event_rule_proposals (`+selectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, string(p.Kind), string(p.TargetKind), p.Target, p.Phrase, p.TypeName, p.Description,
		evidenceJSON, string(p.Status), p.PromptVersion, p.CreatedAt.UTC().Format(timeFormat), nullableTime(p.DecidedAt),
		windowOrDefault(p.TypeWindow), p.TypeReferential); err != nil {
		return fmt.Errorf("sqliteeventruleproposal: insert: %w", err)
	}
	return nil
}

// windowOrDefault keeps the NOT NULL type_window column on its default for a
// proposal that carries no window (every phrase proposal).
func windowOrDefault(w string) string {
	if w == "" {
		return eventruleproposal.DefaultTypeWindow
	}
	return w
}

// MergeTypes folds the type proposal srcID into dstID in one transaction: the
// source becomes rejected (decided_at = now) and the destination gains the
// source's evidence. Each write is guarded by the expected prior status
// (srcFrom, dstFrom); if either guard misses nothing is written and the error
// wraps eventruleproposalport.ErrStatusChanged. It returns the updated
// destination.
func (r *Repository) MergeTypes(ctx context.Context, srcID string, srcFrom eventruleproposal.Status, dstID string, dstFrom eventruleproposal.Status, now time.Time) (eventruleproposal.Proposal, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	src, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM event_rule_proposals WHERE id = ?`, srcID))
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: merge source %q: %w", srcID, err)
	}
	dst, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM event_rule_proposals WHERE id = ?`, dstID))
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: merge destination %q: %w", dstID, err)
	}
	dst.EvidenceIDs = mergeEvidence(dst.EvidenceIDs, src.EvidenceIDs)
	blob, err := json.Marshal(dst.EvidenceIDs)
	if err != nil {
		return eventruleproposal.Proposal{}, err
	}

	res, err := tx.ExecContext(ctx, `UPDATE event_rule_proposals SET status = ?, decided_at = ? WHERE id = ? AND status = ?`,
		string(eventruleproposal.StatusRejected), nullableTime(now), srcID, string(srcFrom))
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: merge source: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: proposal %q is no longer %s: %w", srcID, srcFrom, eventruleproposalport.ErrStatusChanged)
	}
	res, err = tx.ExecContext(ctx, `UPDATE event_rule_proposals SET evidence_json = ? WHERE id = ? AND status = ?`,
		string(blob), dstID, string(dstFrom))
	if err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: merge destination: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: proposal %q is no longer %s: %w", dstID, dstFrom, eventruleproposalport.ErrStatusChanged)
	}
	if err := tx.Commit(); err != nil {
		return eventruleproposal.Proposal{}, fmt.Errorf("sqliteeventruleproposal: commit: %w", err)
	}
	return dst, nil
}

// ReplaceTypes deletes the proposals deleteIDs and inserts the given ones in
// one transaction. Each inserted proposal is normalized and gets the ID
// Upsert would assign it.
func (r *Repository) ReplaceTypes(ctx context.Context, deleteIDs []string, insert []eventruleproposal.Proposal) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqliteeventruleproposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range deleteIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM event_rule_proposals WHERE id = ?`, id); err != nil {
			return fmt.Errorf("sqliteeventruleproposal: delete %q: %w", id, err)
		}
	}
	for _, p := range insert {
		p = normalize(p)
		p.ID = proposalID(p)
		blob, err := json.Marshal(mergeEvidence(nil, p.EvidenceIDs))
		if err != nil {
			return err
		}
		if err := insertProposal(ctx, tx, p, string(blob)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqliteeventruleproposal: commit: %w", err)
	}
	return nil
}

// RetargetPhrases re-keys, in one transaction, each pending or approved
// action phrase proposal whose target is from so it targets to. A phrase
// proposal's ID is derived from its key, so each one is deleted and inserted
// again under the ID of its new key, keeping its status, evidence, prompt
// version and timestamps; when a proposal with the new key already exists,
// the evidence is merged into it instead (its status is kept). It returns
// the re-keyed proposals as stored.
func (r *Repository) RetargetPhrases(ctx context.Context, from, to string) ([]eventruleproposal.Proposal, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqliteeventruleproposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT `+selectColumns+` FROM event_rule_proposals
		WHERE kind = ? AND target_kind = ? AND target = ? AND status IN (?, ?) ORDER BY created_at, id`,
		string(eventruleproposal.KindPhrase), string(eventruleproposal.TargetAction), from,
		string(eventruleproposal.StatusPending), string(eventruleproposal.StatusApproved))
	if err != nil {
		return nil, fmt.Errorf("sqliteeventruleproposal: list phrases of %q: %w", from, err)
	}
	var olds []eventruleproposal.Proposal
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		olds = append(olds, p)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("sqliteeventruleproposal: list phrases of %q: %w", from, err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqliteeventruleproposal: list phrases of %q: %w", from, err)
	}

	out := make([]eventruleproposal.Proposal, 0, len(olds))
	for _, old := range olds {
		p := normalize(old)
		p.Target = strings.TrimSpace(to)
		p.ID = proposalID(p)
		if _, err := tx.ExecContext(ctx, `DELETE FROM event_rule_proposals WHERE id = ?`, old.ID); err != nil {
			return nil, fmt.Errorf("sqliteeventruleproposal: delete %q: %w", old.ID, err)
		}
		existing, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM event_rule_proposals WHERE id = ?`, p.ID))
		switch {
		case err == nil:
			existing.EvidenceIDs = mergeEvidence(existing.EvidenceIDs, p.EvidenceIDs)
			blob, err := json.Marshal(existing.EvidenceIDs)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE event_rule_proposals SET evidence_json = ? WHERE id = ?`, string(blob), existing.ID); err != nil {
				return nil, fmt.Errorf("sqliteeventruleproposal: merge evidence: %w", err)
			}
			out = append(out, existing)
		case errors.Is(err, sql.ErrNoRows):
			p.EvidenceIDs = mergeEvidence(nil, p.EvidenceIDs)
			blob, err := json.Marshal(p.EvidenceIDs)
			if err != nil {
				return nil, err
			}
			if err := insertProposal(ctx, tx, p, string(blob)); err != nil {
				return nil, err
			}
			out = append(out, p)
		default:
			return nil, fmt.Errorf("sqliteeventruleproposal: lookup %q: %w", p.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqliteeventruleproposal: commit: %w", err)
	}
	return out, nil
}

// RejectPhrasesFor rejects (decided_at = now) every pending or approved
// action phrase proposal whose target is target and returns how many.
func (r *Repository) RejectPhrasesFor(ctx context.Context, target string, now time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE event_rule_proposals SET status = ?, decided_at = ?
		WHERE kind = ? AND target_kind = ? AND target = ? AND status IN (?, ?)`,
		string(eventruleproposal.StatusRejected), nullableTime(now),
		string(eventruleproposal.KindPhrase), string(eventruleproposal.TargetAction), target,
		string(eventruleproposal.StatusPending), string(eventruleproposal.StatusApproved))
	if err != nil {
		return 0, fmt.Errorf("sqliteeventruleproposal: reject phrases of %q: %w", target, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqliteeventruleproposal: reject phrases of %q: %w", target, err)
	}
	return int(n), nil
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

// Save persists the status and decided_at of an existing proposal whatever
// its stored status, for seeding fixtures; decisions use Transition.
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

// Transition implements eventruleproposalport.Repository: the update is
// guarded by the expected prior status, so of two racing decisions only one
// lands.
func (r *Repository) Transition(ctx context.Context, p eventruleproposal.Proposal, from eventruleproposal.Status) error {
	res, err := r.db.ExecContext(ctx, `UPDATE event_rule_proposals SET status = ?, decided_at = ? WHERE id = ? AND status = ?`,
		string(p.Status), nullableTime(p.DecidedAt), p.ID, string(from))
	if err != nil {
		return fmt.Errorf("sqliteeventruleproposal: transition: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqliteeventruleproposal: transition: %w", err)
	}
	if n > 0 {
		return nil
	}
	cur, err := r.Get(ctx, p.ID)
	if err != nil {
		return err
	}
	return fmt.Errorf("sqliteeventruleproposal: proposal %q is %s, not %s: %w", p.ID, cur.Status, from, eventruleproposalport.ErrStatusChanged)
}

// TransitionType is Transition for a type approval: besides status and
// decided_at it writes the proposal's TypeWindow and TypeReferential, guarded
// by the same expected prior status.
func (r *Repository) TransitionType(ctx context.Context, p eventruleproposal.Proposal, from eventruleproposal.Status) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE event_rule_proposals SET status = ?, decided_at = ?, type_window = ?, type_referential = ? WHERE id = ? AND status = ?`,
		string(p.Status), nullableTime(p.DecidedAt), windowOrDefault(p.TypeWindow), p.TypeReferential, p.ID, string(from))
	if err != nil {
		return fmt.Errorf("sqliteeventruleproposal: transition type: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqliteeventruleproposal: transition type: %w", err)
	}
	if n > 0 {
		return nil
	}
	cur, err := r.Get(ctx, p.ID)
	if err != nil {
		return err
	}
	return fmt.Errorf("sqliteeventruleproposal: proposal %q is %s, not %s: %w", p.ID, cur.Status, from, eventruleproposalport.ErrStatusChanged)
}
