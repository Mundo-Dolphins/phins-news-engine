package out

import (
	"context"
	"time"
)

// ReviewDecision is one human decision made during an editorial-cycle
// review session — a journal entry distinct from human_labels (see Task
// 1): every Persist-able decision (see domain/reviewdecision) also writes
// through to human_labels, but every decision (including ambiguous/
// edit_category/skip) is recorded here regardless, so a session can
// resume without re-presenting an already-decided article.
type ReviewDecision struct {
	SessionID string
	ArticleID string
	Action    string
	Label     string
	Reason    string
	Notes     string
	DecidedAt time.Time
}

// ReviewDecisionRepository persists per-session review decisions.
type ReviewDecisionRepository interface {
	Save(ctx context.Context, d ReviewDecision) error
	// ListBySession returns every decision for sessionID, oldest first.
	ListBySession(ctx context.Context, sessionID string) ([]ReviewDecision, error)
	// ListRecentByAction returns the distinct article IDs with a decision
	// of exactly action at or after since, across every session — used to
	// exclude recently-"ambiguous" articles from re-selection (the
	// active-learning cooldown).
	ListRecentByAction(ctx context.Context, action string, since time.Time) ([]string, error)
}
