// infra/eventrules/proposals.go
package eventrules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// maxBriefExamples bounds the evidence titles quoted in a brief.
const maxBriefExamples = 5

var (
	// ErrNotTypeProposal reports a phrase proposal where a type proposal is
	// required; errors.Is matches it.
	ErrNotTypeProposal = errors.New("not a type proposal")
	// ErrNoBrief reports a type proposal whose status is neither pending nor
	// approved; errors.Is matches it.
	ErrNoBrief = errors.New("proposal has no brief")
)

// textError carries a fixed message and matches a sentinel through errors.Is.
type textError struct {
	msg      string
	sentinel error
}

func (e *textError) Error() string { return e.msg }
func (e *textError) Unwrap() error { return e.sentinel }

// ListTypes returns the pending, then the approved, event type proposals.
func ListTypes(ctx context.Context, db *sql.DB) ([]eventruleproposal.Proposal, error) {
	repo := sqliteeventruleproposal.NewRepository(db)
	var out []eventruleproposal.Proposal
	for _, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved} {
		ps, err := repo.ListByStatus(ctx, st)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if p.Kind == eventruleproposal.KindType {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// DecideType approves or rejects the type proposal id at now and stores the
// result. A phrase proposal returns ErrNotTypeProposal, an already decided
// one the domain error, and an unknown id the repository's not-found error
// (errors.Is(err, sql.ErrNoRows)). Nothing is applied: a type needs a code
// change, see Brief.
func DecideType(ctx context.Context, db *sql.DB, id string, approve bool, now time.Time) (eventruleproposal.Proposal, error) {
	repo := sqliteeventruleproposal.NewRepository(db)
	p, err := repo.Get(ctx, id)
	if err != nil {
		return eventruleproposal.Proposal{}, err
	}
	if p.Kind != eventruleproposal.KindType {
		return eventruleproposal.Proposal{}, &textError{
			msg:      fmt.Sprintf("proposal %s is a %s proposal, not a type proposal; phrases are decided in `events review`", p.ID, p.Kind),
			sentinel: ErrNotTypeProposal,
		}
	}
	decide := eventruleproposal.Proposal.Reject
	if approve {
		decide = eventruleproposal.Proposal.Approve
	}
	decided, err := decide(p, now)
	if err != nil {
		return eventruleproposal.Proposal{}, err
	}
	if err := repo.Save(ctx, decided); err != nil {
		return eventruleproposal.Proposal{}, err
	}
	return decided, nil
}

// Brief returns the implementation brief of the pending or approved type
// proposal id. A phrase proposal returns ErrNotTypeProposal, any other
// status ErrNoBrief, an unknown id the repository's not-found error.
func Brief(ctx context.Context, db *sql.DB, id string) (string, error) {
	p, err := sqliteeventruleproposal.NewRepository(db).Get(ctx, id)
	if err != nil {
		return "", err
	}
	if p.Kind != eventruleproposal.KindType {
		return "", &textError{
			msg:      fmt.Sprintf("proposal %s is a %s proposal, not a type proposal", p.ID, p.Kind),
			sentinel: ErrNotTypeProposal,
		}
	}
	if p.Status != eventruleproposal.StatusPending && p.Status != eventruleproposal.StatusApproved {
		return "", &textError{
			msg:      fmt.Sprintf("proposal %s is %s; only pending or approved type proposals have a brief", p.ID, p.Status),
			sentinel: ErrNoBrief,
		}
	}
	articles := sqliteadapter.NewArticleRepository(db)
	var examples []string
	for _, eid := range p.EvidenceIDs {
		if len(examples) >= maxBriefExamples {
			break
		}
		a, err := articles.FindByID(ctx, eid)
		if err != nil || a.Title == "" {
			continue // evidence may have been pruned
		}
		examples = append(examples, a.Title)
	}
	return eventrulecycle.Brief(p, examples), nil
}
