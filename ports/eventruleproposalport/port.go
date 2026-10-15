// ports/eventruleproposalport/port.go

// Package eventruleproposalport declares the driven port for persisting
// LLM-proposed event rules. It lives in its own package, like
// entityproposalport, so the ingestion binary's dependency graph never
// picks up the discovery domain by accident.
package eventruleproposalport

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// Repository persists event rule proposals.
type Repository interface {
	// Upsert stores p keyed by its logical rule (kind, target kind, target,
	// phrase, type name; case-insensitive). A pending match has its evidence
	// merged; a match in any other status is returned unchanged (a rejected
	// proposal is never resurrected). The returned proposal carries the
	// stored ID.
	Upsert(ctx context.Context, p eventruleproposal.Proposal) (eventruleproposal.Proposal, error)
	ListByStatus(ctx context.Context, s eventruleproposal.Status) ([]eventruleproposal.Proposal, error)
	// Get returns the proposal or an error wrapping sql.ErrNoRows.
	Get(ctx context.Context, id string) (eventruleproposal.Proposal, error)
	// Save persists the status and decided_at of an existing proposal.
	Save(ctx context.Context, p eventruleproposal.Proposal) error
}
