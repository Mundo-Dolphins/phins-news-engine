// app/entitycycle/run.go
package entitycycle

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Deps are the collaborators of the step.
type Deps struct {
	Lister     entitydiscovery.ArticleLister
	LLM        appout.EntityDiscoveryLLM
	Proposals  entityproposalport.EntityProposalRepository
	Catalog    entitydiscovery.CatalogIndex
	Verifier   entitydiscovery.Verifier // nil = no verification
	League     *leaguegazetteer.Index   // nil = none
	Apply      func(p entityproposal.Proposal, now time.Time) error
	Regenerate func() error
	Now        func() time.Time
}

// Input is one invocation.
type Input struct {
	SessionArticleIDs   []string
	ExtractorVersion    string
	BatchSize, Limit    int
	VerifyMinConfidence float64
	Scanner             *bufio.Scanner
	Out                 io.Writer
}

// Result summarises the step for the audit report.
type Result struct {
	Articles, Batches, BatchesFailed, Proposed int
	Approved, Rejected, Skipped                int
	DiscoveryFailed, Paused, Regenerated       bool

	// catalogDirty is set as soon as an approval edited the catalog files, even
	// if recording the approval then failed, so they are always regenerated.
	catalogDirty bool
}

// Run discovers entities in the session's articles and walks the operator
// through the resulting proposals. An unreachable discovery LLM is a warning,
// never an error: the cycle continues without entities.
func Run(ctx context.Context, d Deps, in Input) (Result, error) {
	var res Result
	now := d.Now
	if now == nil {
		now = time.Now
	}

	dr, err := entitydiscovery.Discover(ctx, d.Lister, d.LLM, d.Proposals, d.Catalog, entitydiscovery.DiscoverOptions{
		BatchSize: in.BatchSize, Limit: in.Limit, ExtractorVersion: in.ExtractorVersion,
		VerifyMinConfidence: in.VerifyMinConfidence, League: d.League,
	}, now().UTC(), d.Verifier)
	if err != nil {
		return res, fmt.Errorf("entitycycle: discover: %w", err)
	}
	res.Articles, res.Batches, res.BatchesFailed, res.Proposed = dr.ArticlesConsidered, dr.BatchesTotal, dr.BatchesFailed, dr.ProposalsUpserted
	fmt.Fprintf(in.Out, "entities: %d articles, %d batches (%d failed), %d proposals created or updated\n", res.Articles, res.Batches, res.BatchesFailed, res.Proposed)

	if res.Batches > 0 && res.BatchesFailed == res.Batches {
		res.DiscoveryFailed = true
		cause := "unknown error"
		if len(dr.BatchErrors) > 0 {
			cause = dr.BatchErrors[0]
		}
		fmt.Fprintf(in.Out, "WARNING: entity discovery is unavailable (%s); no new entities this cycle. The articles stay unprocessed and a later run retries them.\n", cause)
	} else if res.BatchesFailed > 0 {
		fmt.Fprintf(in.Out, "WARNING: %d of %d discovery batches failed; their articles stay unprocessed for a later run.\n", res.BatchesFailed, res.Batches)
	}

	// Proposals already pending for this session (from an earlier, interrupted
	// run) need no LLM, so they are offered even after an outage.
	reviewErr := review(ctx, d, in, now, &res)
	// Regenerate whenever the files were edited, even if the review then failed.
	if res.catalogDirty && d.Regenerate != nil {
		if err := d.Regenerate(); err != nil {
			return res, errors.Join(reviewErr, fmt.Errorf("entitycycle: regenerate catalog: %w", err))
		}
		res.Regenerated = true
		fmt.Fprintln(in.Out, "entity catalog regenerated")
	}
	return res, reviewErr
}

func review(ctx context.Context, d Deps, in Input, now func() time.Time, res *Result) error {
	inSession := make(map[string]bool, len(in.SessionArticleIDs))
	for _, id := range in.SessionArticleIDs {
		inSession[id] = true
	}
	all, err := d.Proposals.List(ctx, "", string(entityproposal.StatusPending))
	if err != nil {
		return fmt.Errorf("entitycycle: list pending proposals: %w", err)
	}
	var mine []entityproposal.Proposal
	for _, p := range all {
		for _, s := range p.Sources {
			if inSession[s.ArticleID] {
				mine = append(mine, p)
				break
			}
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return entityproposal.Score(mine[i]) > entityproposal.Score(mine[j]) })
	if len(mine) == 0 {
		fmt.Fprintln(in.Out, "No pending entity proposals for this session.")
		return nil
	}

	for i, p := range mine {
		printProposal(in.Out, i+1, len(mine), p)
		for {
			prompt := "Action: [a]pprove, [r]eject, [s]kip, [q]uit: "
			if p.Kind == entityproposal.KindPossibleDuplicate {
				prompt = "Action: approve as [n]ew entity, [r]eject, [s]kip, [q]uit: "
			}
			fmt.Fprint(in.Out, prompt)
			if !in.Scanner.Scan() {
				res.Paused = true
				fmt.Fprintf(in.Out, "\nEntity review paused. approved: %d, rejected: %d, skipped: %d\n", res.Approved, res.Rejected, res.Skipped)
				return nil
			}
			action := strings.ToLower(strings.TrimSpace(in.Scanner.Text()))
			advance, stop, err := handle(ctx, d, in, now, p, action, res)
			if err != nil {
				return err
			}
			if stop {
				res.Paused = true
				fmt.Fprintf(in.Out, "Entity review paused. approved: %d, rejected: %d, skipped: %d\n", res.Approved, res.Rejected, res.Skipped)
				return nil
			}
			if advance {
				break
			}
		}
	}
	fmt.Fprintf(in.Out, "Entity review complete. approved: %d, rejected: %d, skipped: %d\n", res.Approved, res.Rejected, res.Skipped)
	return nil
}

// handle applies one action; advance moves to the next proposal, stop pauses.
func handle(ctx context.Context, d Deps, in Input, now func() time.Time, p entityproposal.Proposal, action string, res *Result) (advance, stop bool, err error) {
	switch action {
	case "a", "approve", "n", "new":
		if p.Kind == entityproposal.KindPossibleDuplicate && action != "n" && action != "new" {
			fmt.Fprintln(in.Out, "This is a possible duplicate. Choose n to approve it as a new entity, or skip and use `entities approve --merge-into` later.")
			return false, false, nil
		}
		resolved := p
		if p.Kind == entityproposal.KindPossibleDuplicate {
			resolved.Kind = entityproposal.KindNewEntity
		}
		if err := d.Apply(resolved, now().UTC()); err != nil {
			fmt.Fprintf(in.Out, "Cannot approve %s: %v\nThe proposal remains pending; choose reject, skip, or another action.\n", p.CanonicalName, err)
			return false, false, nil
		}
		res.catalogDirty = true
		if err := d.Proposals.UpdateStatus(ctx, p.ID, entityproposal.StatusApproved, now().UTC()); err != nil {
			return false, false, fmt.Errorf("entitycycle: approve %s: %w", p.ID, err)
		}
		fmt.Fprintf(in.Out, "Approved %s.\n", p.CanonicalName)
		res.Approved++
		return true, false, nil
	case "r", "reject":
		if err := d.Proposals.UpdateStatus(ctx, p.ID, entityproposal.StatusRejected, now().UTC()); err != nil {
			return false, false, fmt.Errorf("entitycycle: reject %s: %w", p.ID, err)
		}
		fmt.Fprintf(in.Out, "Rejected %s.\n", p.CanonicalName)
		res.Rejected++
		return true, false, nil
	case "s", "skip", "":
		res.Skipped++
		return true, false, nil
	case "q", "quit":
		return false, true, nil
	default:
		fmt.Fprintln(in.Out, "Unknown action. Choose a, r, s, or q.")
		return false, false, nil
	}
}

func printProposal(w io.Writer, index, total int, p entityproposal.Proposal) {
	fmt.Fprintf(w, "\n--- Entity proposal %d/%d ---\n", index, total)
	fmt.Fprintf(w, "%s · %s · %s · confidence %.2f · occurrences %d\n", p.CanonicalName, p.EntityType, p.Kind, p.Confidence, p.Occurrences)
	if len(p.Aliases) > 0 {
		fmt.Fprintf(w, "Aliases: %s\n", strings.Join(p.Aliases, ", "))
	}
	for _, k := range []string{"team", "position", "role", "league", "suggested_entity", "suggested_team"} {
		if v := p.Metadata[k]; v != "" {
			fmt.Fprintf(w, "%s: %s\n", k, v)
		}
	}
	for _, s := range p.Sources {
		fmt.Fprintf(w, "Source: %s\n", s.ArticleTitle)
	}
}
