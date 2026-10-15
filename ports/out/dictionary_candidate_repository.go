package out

import (
	"context"
	"time"
)

// DictionaryCandidate is one mined keyword candidate awaiting (or having
// received) a human decision during `dictionary update` — see
// application/dictionarymining.
type DictionaryCandidate struct {
	Term             string
	SessionID        string
	CategoryGuess    string // "" when the mining heuristic had no guess
	SupportCount     int
	FirstSeenArticle string
	Status           string // pending|approved|rejected
	DecidedAt        time.Time
}

// DictionaryCandidateRepository persists mined dictionary candidates.
type DictionaryCandidateRepository interface {
	// Save inserts c if term doesn't already exist; a term already
	// present (e.g. from a prior, interrupted `dictionary update` run
	// against the same session) is left untouched — mining is safe to
	// re-run without clobbering an already-decided candidate.
	Save(ctx context.Context, c DictionaryCandidate) error
	// ListBySession returns every candidate for sessionID, highest
	// support_count first.
	ListBySession(ctx context.Context, sessionID string) ([]DictionaryCandidate, error)
	// ListRejectedTerms returns rejections across every session. Mining uses
	// these as case-insensitive exclusions, including in dry-run previews.
	ListRejectedTerms(ctx context.Context) ([]string, error)
	// UpdateDecision sets term's Status, CategoryGuess (the final,
	// possibly human-overridden category), and DecidedAt.
	UpdateDecision(ctx context.Context, term, status, categoryGuess string, decidedAt time.Time) error
}
