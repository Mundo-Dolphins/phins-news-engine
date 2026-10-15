// Package entityproposalport declares the driven port for persisting
// LLM-discovered entity-catalog proposals.
//
// It is deliberately kept out of ports/out: that
// package is a single Go package imported wholesale by the service binary
// (the ingestion binary) for its real ports (ArticleRepository, etc.), and
// Go compiles a package as one unit — so this interface's dependency on
// domain/entityproposal would otherwise taint the service binary's
// entire transitive dependency graph even though the ingestion binary
// never constructs or calls an EntityProposalRepository. Living in its own
// package guarantees that can never happen, structurally rather than by
// convention.
package entityproposalport

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// EntityProposalRepository persists LLM-discovered entity-catalog
// candidates and tracks which articles `entities discover`
// has already analyzed for a given extractor version.
type EntityProposalRepository interface {
	// Upsert merges p into an existing pending proposal sharing
	// p.NormalizedName+p.EntityType (summing Occurrences, unioning
	// Aliases/Sources, widening First/LastSeen, keeping the higher
	// Confidence) if one exists, or inserts p as a new pending proposal
	// otherwise — unless a row with that identity was automatically
	// rejected (auto_rejected_non_nfl, duplicate_of_existing, not_an_nfl_team or
	// auto_rejected_noise), whose id
	// is returned unchanged instead of re-queuing it. Returns the affected
	// proposal's id (which may differ from p.ID when merged into a
	// pre-existing row).
	Upsert(ctx context.Context, p entityproposal.Proposal) (string, error)
	// Get returns the proposal with the given id, or a wrapped
	// entityproposal.ErrNotFound if none exists.
	Get(ctx context.Context, id string) (entityproposal.Proposal, error)
	// FindByNormalizedName looks up a proposal (any status: pending,
	// approved, or rejected) sharing normalizedName+entityType. Used by
	// the online resolution fallback as its cache:
	// finding one — of any status — means "don't call the LLM again for
	// this candidate," since even a rejected proposal is a more reliable
	// signal than re-asking. Returns (Proposal{}, false, nil), not
	// entityproposal.ErrNotFound, when nothing matches.
	FindByNormalizedName(ctx context.Context, normalizedName string, entityType entityproposal.EntityType) (entityproposal.Proposal, bool, error)
	// FindByAlias looks up a proposal (any status) of entityType whose
	// Aliases contains an entry a such that entityproposal.Normalize(a) ==
	// normalizedAlias. Used alongside FindByNormalizedName by the online
	// resolution fallback's cache lookup: a
	// candidate's own raw span is recorded as one of its proposal's
	// aliases (see entityresolution.persistEvidence), so a later
	// occurrence of the SAME raw span hits the cache even though the
	// proposal itself is keyed by the LLM's canonical name, not the
	// span. Returns (Proposal{}, false, nil), not entityproposal.ErrNotFound,
	// when nothing matches.
	FindByAlias(ctx context.Context, normalizedAlias string, entityType entityproposal.EntityType) (entityproposal.Proposal, bool, error)
	// List returns every proposal matching entityType and status
	// (either may be "" to mean "no filter on that field"), oldest
	// created_at first.
	List(ctx context.Context, entityType, status string) ([]entityproposal.Proposal, error)
	// UpdateStatus sets id's Status and DecidedAt. Returns a wrapped
	// entityproposal.ErrNotFound if id doesn't exist.
	UpdateStatus(ctx context.Context, id string, status entityproposal.Status, decidedAt time.Time) error
	// UpdateVerification replaces a proposal's verification-derived fields
	// (entity type, canonical/normalized name, metadata, confidence,
	// aliases, web sources, status, decision reason) in one transaction and
	// sets decided_at when p.Status is not pending. Only a row that is
	// still pending is written. Returns a wrapped
	// entityproposal.ErrNotFound for an unknown id and
	// entityproposal.ErrConflict if the row is no longer pending (decided
	// meanwhile) or the corrected identity collides with another pending
	// proposal.
	UpdateVerification(ctx context.Context, p entityproposal.Proposal) error
	// FilterUnprocessed returns the subset of articleIDs that have no
	// row in entity_discovery_processed_articles for extractorVersion.
	FilterUnprocessed(ctx context.Context, extractorVersion string, articleIDs []string) ([]string, error)
	// MarkArticlesProcessed records articleIDs as processed for
	// extractorVersion. Idempotent: an id already marked is left
	// unchanged.
	MarkArticlesProcessed(ctx context.Context, extractorVersion string, articleIDs []string, processedAt time.Time) error
}
