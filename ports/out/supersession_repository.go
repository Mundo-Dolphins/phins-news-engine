package out

import (
	"context"
	"time"
)

// SupersededItem is a claimable publication invalidated by a terminal match
// state. The operation is performed in the caller's UnitOfWork transaction.
type SupersededItem struct {
	ArticleID  string
	FromStatus string
}

// TerminalSupersessionRepository atomically moves claimable earlier game
// publications out of the outbox and marks their articles discarded.
type TerminalSupersessionRepository interface {
	SupersedeClaimableMatch(ctx context.Context, matchID, finalArticleID string, now time.Time) ([]SupersededItem, error)
}
