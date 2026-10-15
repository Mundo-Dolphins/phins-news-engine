// app/entitydiscovery/evaluate.go
package entitydiscovery

import (
	"context"
	"fmt"
	"math/rand"
	"sort"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// Assessor is the Wikidata stage as evaluation sees it (ChainVerifier).
type Assessor interface {
	Assess(ctx context.Context, c VerifyCandidate) Assessment
}

// ProposalLister is the only repository capability Evaluate gets, so it
// cannot mutate proposals by construction.
type ProposalLister interface {
	List(ctx context.Context, entityType, status string) ([]entityproposal.Proposal, error)
}

// EvalOptions configures one evaluation run.
type EvalOptions struct {
	Limit      int // 0 = no cap; ignored when Sample > 0
	Status     string
	Kind       string
	EntityType string
	Sample     int // >0: deterministic random subset of this size
	Seed       int64
	Reviewed   map[string]string // proposal id -> "correct" | "wrong"
}

// Evaluate benchmarks the Wikidata stage on stored proposals using their
// stored names and sources. It is read-only with respect to proposals and
// never calls the LLM/SearXNG fallback.
func Evaluate(ctx context.Context, repo ProposalLister, a Assessor, catalog CatalogIndex, opts EvalOptions) (EvalReport, error) {
	var rep EvalReport
	rows, err := repo.List(ctx, opts.EntityType, opts.Status)
	if err != nil {
		return rep, fmt.Errorf("entitydiscovery: list proposals: %w", err)
	}
	var todo []entityproposal.Proposal
	for _, p := range rows {
		if opts.Kind != "" && string(p.Kind) != opts.Kind {
			continue
		}
		if opts.EntityType != "" && string(p.EntityType) != opts.EntityType {
			continue
		}
		todo = append(todo, p)
	}
	if opts.Sample > 0 {
		sort.SliceStable(todo, func(i, j int) bool { return todo[i].ID < todo[j].ID })
		r := rand.New(rand.NewSource(opts.Seed)) // #nosec G404 -- seeded on purpose: reproducible sampling, not security-sensitive.
		r.Shuffle(len(todo), func(i, j int) { todo[i], todo[j] = todo[j], todo[i] })
		if len(todo) > opts.Sample {
			todo = todo[:opts.Sample]
		}
	} else if opts.Limit > 0 && len(todo) > opts.Limit {
		todo = todo[:opts.Limit]
	}
	for _, p := range todo {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		rep.add(classifyProposal(ctx, p, a, catalog))
	}
	rep.finish(opts.Reviewed)
	return rep, nil
}

func classifyProposal(ctx context.Context, p entityproposal.Proposal, a Assessor, catalog CatalogIndex) EvalCase {
	c := EvalCase{ProposalID: p.ID, Name: p.CanonicalName, EntityType: string(p.EntityType), Kind: string(p.Kind)}
	if _, ok := catalog.Lookup(entityproposal.Normalize(p.CanonicalName)); ok {
		c.Outcome = OutcomeLocalCatalog
		return c
	}
	as := a.Assess(ctx, VerifyCandidate{
		Name: p.CanonicalName, Type: p.EntityType, Metadata: candidateMetadata(p.Metadata),
		Confidence: p.Confidence, Sources: p.Sources,
	})
	c.SearchHit = as.Candidates > 0
	switch as.Status {
	case string(entityidentity.StatusVerifiedNFL):
		c.Outcome = OutcomeVerifiedNFL
	case string(entityidentity.StatusVerifiedNonNFL):
		c.Outcome = OutcomeVerifiedNonNFL
	case string(entityidentity.StatusAmbiguous):
		c.Outcome = OutcomeAmbiguous
	case AssessmentError:
		c.Outcome = OutcomeError
	default:
		c.Outcome = OutcomeNotFound
	}
	if as.Decisive {
		md := as.Result.Metadata
		c.ProviderID = as.Result.Provenance.ProviderID
		c.HasTeam = md["team"] != ""
		c.HasPosition = md["position"] != "" || md["role"] != ""
	} else {
		c.FallbackRequired = true
	}
	return c
}
