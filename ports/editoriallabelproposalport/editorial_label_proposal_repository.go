// Package editoriallabelproposalport declares the driven port for
// offline, review-gated editorial label proposals.
package editoriallabelproposalport

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
)

// EditorialLabelProposalRepository stores LLM-generated label
// suggestions and tracks which articles have already been analyzed
// under a given prompt version, so a batch classification run (issue
// #249) can skip articles it already classified.
type EditorialLabelProposalRepository interface {
	// Save inserts a new proposal. Returns
	// editoriallabelproposal.ErrPendingProposalExists if articleID
	// already has a pending proposal -- callers must resolve it first.
	Save(ctx context.Context, p editoriallabelproposal.Proposal) (string, error)
	// GetLatestByArticle returns the most recently created proposal for
	// articleID (any status), or ok=false if none exists.
	GetLatestByArticle(ctx context.Context, articleID string) (p editoriallabelproposal.Proposal, ok bool, err error)
	// ListLatestPerArticle returns, for every article with at least one
	// proposal, that article's most recently created proposal (any
	// status), tie-broken the same way as GetLatestByArticle:
	// created_at DESC, id DESC.
	ListLatestPerArticle(ctx context.Context) ([]editoriallabelproposal.Proposal, error)
	// ListByStatus returns proposals in the given status, oldest first.
	ListByStatus(ctx context.Context, status editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error)
	// UpdateStatus transitions a proposal to status, recording decidedAt.
	// Returns editoriallabelproposal.ErrNotFound if id does not exist.
	UpdateStatus(ctx context.Context, id string, status editoriallabelproposal.Status, decidedAt time.Time) error
	// FilterUnprocessed returns the subset of articleIDs that have not
	// yet been analyzed under promptVersion.
	FilterUnprocessed(ctx context.Context, promptVersion string, articleIDs []string) ([]string, error)
	// MarkArticlesProcessed records that articleIDs were analyzed under
	// promptVersion at processedAt, idempotently.
	MarkArticlesProcessed(ctx context.Context, promptVersion string, articleIDs []string, processedAt time.Time) error
}
