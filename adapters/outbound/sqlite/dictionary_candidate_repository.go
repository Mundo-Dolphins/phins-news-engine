package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DictionaryCandidateRepository implements
// ports/out.DictionaryCandidateRepository against SQLite.
type DictionaryCandidateRepository struct {
	db *sql.DB
}

// NewDictionaryCandidateRepository builds a DictionaryCandidateRepository
// over an already-open, already-migrated database.
func NewDictionaryCandidateRepository(db *sql.DB) *DictionaryCandidateRepository {
	return &DictionaryCandidateRepository{db: db}
}

var _ appout.DictionaryCandidateRepository = (*DictionaryCandidateRepository)(nil)

// Save implements ports/out.DictionaryCandidateRepository.
func (r *DictionaryCandidateRepository) Save(ctx context.Context, c appout.DictionaryCandidate) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO dictionary_candidate_terms (term, session_id, category_guess, support_count, first_seen_article, status)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT (term) DO NOTHING
	`, c.Term, c.SessionID, c.CategoryGuess, c.SupportCount, c.FirstSeenArticle, c.Status)
	if err != nil {
		return fmt.Errorf("sqlite: save dictionary candidate %q: %w", c.Term, err)
	}
	return nil
}

// ListBySession implements ports/out.DictionaryCandidateRepository.
func (r *DictionaryCandidateRepository) ListBySession(ctx context.Context, sessionID string) ([]appout.DictionaryCandidate, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT term, session_id, category_guess, support_count, first_seen_article, status, decided_at
		FROM dictionary_candidate_terms WHERE session_id = ? ORDER BY support_count DESC, term ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list dictionary candidates for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var candidates []appout.DictionaryCandidate
	for rows.Next() {
		var c appout.DictionaryCandidate
		var decidedAt sqliteNullTime
		if err := rows.Scan(&c.Term, &c.SessionID, &c.CategoryGuess, &c.SupportCount, &c.FirstSeenArticle, &c.Status, &decidedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan dictionary candidate: %w", err)
		}
		if decidedAt.Valid {
			c.DecidedAt = decidedAt.Time
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate dictionary candidates: %w", err)
	}
	return candidates, nil
}

// UpdateDecision implements ports/out.DictionaryCandidateRepository.
func (r *DictionaryCandidateRepository) UpdateDecision(ctx context.Context, term, status, categoryGuess string, decidedAt time.Time) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE dictionary_candidate_terms SET status = ?, category_guess = ?, decided_at = ? WHERE term = ?
	`, status, categoryGuess, nullableTime(decidedAt), term)
	if err != nil {
		return fmt.Errorf("sqlite: update dictionary candidate decision for %q: %w", term, err)
	}
	return nil
}

// ListRejectedTerms implements ports/out.DictionaryCandidateRepository.
func (r *DictionaryCandidateRepository) ListRejectedTerms(ctx context.Context) ([]string, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `SELECT term FROM dictionary_candidate_terms WHERE status = 'rejected' ORDER BY term`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list rejected dictionary terms: %w", err)
	}
	defer rows.Close()
	var terms []string
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			return nil, fmt.Errorf("sqlite: scan rejected dictionary term: %w", err)
		}
		terms = append(terms, term)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate rejected dictionary terms: %w", err)
	}
	return terms, nil
}
