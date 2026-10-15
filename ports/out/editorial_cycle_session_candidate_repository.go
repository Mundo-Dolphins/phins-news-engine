package out

import (
	"context"
	"time"
)

// EditorialCycleSessionCandidate is one article frozen into a session's
// review set at creation time.
// Once written for a session, this set never changes for that session's
// lifetime, even across --resume.
type EditorialCycleSessionCandidate struct {
	SessionID  string
	ArticleID  string
	Score      float64
	SelectedAt time.Time
}

// EditorialCycleSessionCandidateRepository persists and queries a
// session's frozen candidate set.
type EditorialCycleSessionCandidateRepository interface {
	// SaveAll writes every candidate for sessionID in one batch, called
	// exactly once, at session creation (never on resume — the whole
	// point is that this set is written once and never touched again).
	SaveAll(ctx context.Context, sessionID string, candidates []EditorialCycleSessionCandidate) error
	// ListPending returns the frozen-set rows for sessionID that have no
	// editorial_review_decisions row yet AND still resolve to a live
	// articles row — the SQL-level anti-join/inner-join the design
	// spec's §6.2 describes, computed in SQL so it stays correct under
	// concurrent writes rather than filtered in Go.
	ListPending(ctx context.Context, sessionID string) ([]EditorialCycleSessionCandidate, error)
	// ListFrozen returns every frozen-set row for sessionID regardless
	// of decision state or whether the article still exists — used by
	// --redecide (which re-presents already-decided candidates) and by
	// corpus-drift auditing (comparing this count to CountResolvable).
	ListFrozen(ctx context.Context, sessionID string) ([]EditorialCycleSessionCandidate, error)
	// CountResolvable returns how many of sessionID's frozen-set rows
	// still join to an existing articles row — 0 means every frozen
	// candidate was purged from the corpus (the session is then cancelled).
	CountResolvable(ctx context.Context, sessionID string) (int, error)
}
