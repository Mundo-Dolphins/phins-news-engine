package editorialcycle

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ExcludeSourceBypasses resolves candidates frozen before a source policy
// change, so they neither prompt the reviewer nor block session completion.
// The skip is audited without creating or retracting any human labels;
// training export independently excludes these sources' existing examples.
func ExcludeSourceBypasses(ctx context.Context, deps Deps, sessionID string, candidates []activelearning.Candidate) ([]activelearning.Candidate, error) {
	for _, c := range candidates {
		if !deps.ExcludedSourceIDs[c.Article.Source.ID.String()] {
			continue
		}
		if err := deps.ReviewDecisions.Save(ctx, out.ReviewDecision{
			SessionID: sessionID, ArticleID: c.Article.ID,
			Action:    string(reviewdecision.ActionSkip),
			Notes:     "source policy bypass: excluded from editorial model review and training",
			DecidedAt: deps.Clock.Now(),
		}); err != nil {
			return nil, fmt.Errorf("editorialcycle: exclude source bypass for %s: %w", c.Article.ID, err)
		}
	}
	return manualReviewCandidates(candidates, deps.ExcludedSourceIDs), nil
}

func manualReviewCandidates(candidates []activelearning.Candidate, excludedSourceIDs map[string]bool) []activelearning.Candidate {
	manual := make([]activelearning.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if !excludedSourceIDs[c.Article.Source.ID.String()] {
			manual = append(manual, c)
		}
	}
	return manual
}
