package out

import (
	"context"
	"time"
)

// EditorialCycleSession is one run of `editorial cycle` — persisted so an
// interrupted run can be resumed.
type EditorialCycleSession struct {
	ID          string
	StartedAt   time.Time
	UpdatedAt   time.Time
	Status      string // running|paused|completed|failed|cancelled
	CurrentStep string
	FlagsJSON   string // JSON snapshot of the CLI flags this run started with
	// TotalCandidates is how many candidates were frozen into this
	// session at creation — the stable baseline corpus drift is measured
	// against. It is written once (session creation, or the one-time
	// legacy frozen-set backfill) and never updated again, because
	// EditorialCycleSessionCandidate rows cascade away when their article
	// is purged: the candidate table can only ever report the survivors,
	// so it cannot tell you how many there originally were. 0 means "no
	// recorded baseline" — either a session created before migration 0025
	// or one that genuinely froze nothing — and suppresses both the drift
	// warning and the all-purged → cancelled transition.
	TotalCandidates int
}

// EditorialCycleSessionRepository persists editorial-cycle session state.
type EditorialCycleSessionRepository interface {
	Create(ctx context.Context, s EditorialCycleSession) error
	Update(ctx context.Context, s EditorialCycleSession) error
	// GetLatestRunning returns the most recently updated session whose
	// Status is "running", or a wrapped sql.ErrNoRows if none exists.
	GetLatestRunning(ctx context.Context) (EditorialCycleSession, error)
	// GetLatestCompleted returns the most recently updated session whose
	// Status is "completed", or a wrapped sql.ErrNoRows if none exists —
	// used by `dictionary update` to default its target session.
	GetLatestCompleted(ctx context.Context) (EditorialCycleSession, error)
	// GetLatestResumable returns the most recently updated session whose
	// Status is "running", "paused", or "failed" — the set StartOrResume
	// picks up on --resume — or a wrapped sql.ErrNoRows if none exists.
	// "completed" and "cancelled" are terminal and never
	// resumed.
	GetLatestResumable(ctx context.Context) (EditorialCycleSession, error)
}
