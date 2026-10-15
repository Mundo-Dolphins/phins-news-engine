package usecase

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SourcePolicySimulation is what `source-policy simulate --article-id`
// prints: every recorded source-policy decision for one article, in
// order, so an operator can see exactly what the factual dedup result,
// the standard policy, and the special policy each said.
type SourcePolicySimulation struct {
	ArticleID string
	SourceID  string
	Decisions []out.SourcePolicyDecisionRecord
}

// SimulateSourcePolicy looks up articleID and every source-policy
// decision recorded for it.
func SimulateSourcePolicy(ctx context.Context, repo out.SourcePolicyDecisionRepository, articles out.ArticleRepository, articleID string) (SourcePolicySimulation, error) {
	a, err := articles.FindByID(ctx, articleID)
	if err != nil {
		return SourcePolicySimulation{}, fmt.Errorf("simulate source policy: find article %s: %w", articleID, err)
	}
	decisions, err := repo.ByArticle(ctx, articleID)
	if err != nil {
		return SourcePolicySimulation{}, fmt.Errorf("simulate source policy: %w", err)
	}
	return SourcePolicySimulation{ArticleID: articleID, SourceID: a.Source.ID.String(), Decisions: decisions}, nil
}
