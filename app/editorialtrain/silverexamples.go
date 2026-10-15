package editorialtrain

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// FetchSilverExamples returns a TrainingExample (Tier=silver) for
// every article whose latest proposal computes to
// editoriallabelproposal.TierSilver, plus a count of Silver candidates
// skipped because their article no longer exists in live storage
// (e.g. deleted by retention) -- unlike Gold, Silver has no durable
// content snapshot to fall back on.
//
// Proposals labeled "uncertain" are excluded before ComputeTier even
// runs: Confidence and Label are set independently by the LLM teacher
// (see app/editorialteacher), so a high-confidence
// "uncertain" proposal is possible, and "uncertain" has no sound
// mapping onto the trainer's binary publish/reject vocabulary.
func FetchSilverExamples(
	ctx context.Context,
	proposals editoriallabelproposalport.EditorialLabelProposalRepository,
	labels out.HumanLabelRepository,
	articles out.ArticleRepository,
	trustedSourceIDs map[string]bool,
	autoLabelThreshold float64,
) ([]TrainingExample, int, error) {
	activeLabels, err := labels.ListAll(ctx)
	if err != nil {
		return nil, 0, err
	}
	hasActiveHumanLabel := make(map[string]bool, len(activeLabels))
	for _, e := range activeLabels {
		hasActiveHumanLabel[e.ArticleID] = true
	}

	latest, err := proposals.ListLatestPerArticle(ctx)
	if err != nil {
		return nil, 0, err
	}

	var examples []TrainingExample
	skippedMissingArticle := 0
	for _, p := range latest {
		if p.Label == "uncertain" {
			continue
		}
		tier := editoriallabelproposal.ComputeTier(hasActiveHumanLabel[p.ArticleID], &p, autoLabelThreshold)
		if tier != editoriallabelproposal.TierSilver {
			continue
		}

		var label string
		switch p.Label {
		case "publish":
			label = "publish"
		case "discard":
			label = "reject"
		default:
			// Unrecognized label value -- can only happen if
			// editoriallabelproposal.Proposal.Label's vocabulary ever
			// changes upstream ("uncertain" is already filtered above).
			// Skip the candidate rather than silently training it as
			// "reject", or erroring the whole export over one bad row.
			continue
		}

		a, err := articles.FindByID(ctx, p.ArticleID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				skippedMissingArticle++
				continue
			}
			return nil, 0, err
		}

		examples = append(examples, NewTrainingExample(
			a, trustedSourceIDs[a.Source.ID.String()], label, string(editoriallabelproposal.TierSilver), p.Reason, p.CreatedAt,
		))
	}
	return examples, skippedMissingArticle, nil
}
