// app/editorialreview/sync.go
//
// Package editorialreview orchestrates review-priority scoring: gathering each pending proposal's NB/LLM
// facts and evidence-table signals, then calling the pure
// reviewpriority.Score and upserting/deleting the corresponding
// review_queue row.
package editorialreview

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

// ArticleGetter is the read access Sync needs over stored articles —
// satisfied by *sqlite.ArticleRepository.FindByID.
type ArticleGetter interface {
	FindByID(ctx context.Context, id string) (article.Article, error)
}

// SyncResult summarizes one Sync run for the CLI to print.
type SyncResult struct {
	ProposalsConsidered int
	Queued              int
	Skipped             int
	// Orphaned counts pending proposals whose article no longer exists
	// (deleted by a retention purge; editorial_label_proposals has no FK
	// to articles by design). These are dropped rather than treated as a
	// fatal error, since retention purges are routine and must not
	// permanently break every future Sync run.
	Orphaned int
}

// Sync recomputes review_queue from every currently-pending
// editorial_label_proposals row: for each, it derives the production NB
// zone/value from the article's stored EditorialScore, gathers cheap
// evidence signals, and calls reviewpriority.Score. An obvious agreement
// (Score.Skip) has its queue row removed (or never inserted); anything
// else is upserted. Sync is idempotent and safe to call after every
// `editorial classify` run regardless of which articles were newly
// classified in that run.
//
// hardExamples is nil-safe, matching trainrun.Deps.HardExamples: a nil
// repository simply leaves Signals.HardMiningCount at 0 for every
// proposal, exactly as if `train mine` had never run.
func Sync(
	ctx context.Context,
	proposals editoriallabelproposalport.EditorialLabelProposalRepository,
	articles ArticleGetter,
	sources appout.SourceRepository,
	signals reviewqueueport.ReviewSignalsRepository,
	queue reviewqueueport.ReviewQueueRepository,
	hardExamples appout.HardExampleRepository,
	model editorial.Model,
	cfg reviewpriority.Config,
	now time.Time,
) (SyncResult, error) {
	pending, err := proposals.ListByStatus(ctx, editoriallabelproposal.StatusPending)
	if err != nil {
		return SyncResult{}, fmt.Errorf("editorialreview: list pending proposals: %w", err)
	}

	sourceFirstSeen, err := loadSourceFirstSeen(ctx, sources)
	if err != nil {
		return SyncResult{}, fmt.Errorf("editorialreview: load sources: %w", err)
	}

	result := SyncResult{ProposalsConsidered: len(pending)}
	for _, p := range pending {
		a, err := articles.FindByID(ctx, p.ArticleID)
		if err != nil {
			if errors.Is(err, article.ErrNotFound) {
				// The article was purged (e.g. by retention) after the
				// proposal was created; editorial_label_proposals has no
				// FK to articles by design. Drop the orphaned queue row,
				// if any, and move on rather than aborting every future
				// Sync run.
				if delErr := queue.Delete(ctx, p.ArticleID); delErr != nil {
					return result, fmt.Errorf("editorialreview: delete orphaned queue row for %s: %w", p.ArticleID, delErr)
				}
				result.Orphaned++
				continue
			}
			return result, fmt.Errorf("editorialreview: find article %s: %w", p.ArticleID, err)
		}

		unknownEntities, err := signals.CountUnknownEntities(ctx, p.ArticleID)
		if err != nil {
			return result, fmt.Errorf("editorialreview: count unknown entities for %s: %w", p.ArticleID, err)
		}
		pendingDictionary, err := signals.CountPendingDictionaryProposals(ctx, p.ArticleID)
		if err != nil {
			return result, fmt.Errorf("editorialreview: count dictionary proposals for %s: %w", p.ArticleID, err)
		}
		// hardMining is 0/1, not a count: hard_example_candidates is keyed
		// by article_id, so an article is either an unresolved hard-mining
		// candidate or it is not. reviewpriority.Score only ever tests
		// HardMiningCount > 0.
		hardMining := 0
		if hardExamples != nil {
			isHard, err := hardExamples.IsUnresolved(ctx, p.ArticleID)
			if err != nil {
				return result, fmt.Errorf("editorialreview: check hard example candidate for %s: %w", p.ArticleID, err)
			}
			if isHard {
				hardMining = 1
			}
		}
		firstSeen, sourceKnown := sourceFirstSeen[a.Source.ID.String()]

		nbZone := zoneFor(a.EditorialScore, model.Thresholds)
		if a.ClassifierVersion == "" {
			// The article was never scored by the NB classifier (e.g.
			// classification was disabled, or the article predates the
			// classifier). EditorialScore's zero value is
			// indistinguishable from a genuine confident-reject score of
			// 0, so ClassifierVersion is the only reliable signal that
			// no NB evaluation happened. Treat "never evaluated" as
			// ambiguous, never as a confident zone -- otherwise it can
			// combine with a confident LLM verdict to look like an
			// "obvious agreement" and silently skip a genuinely
			// unreviewed article.
			nbZone = editorial.ZoneAmbiguous
		}

		in := reviewpriority.Input{
			NBZone:          nbZone,
			NBValue:         a.EditorialScore,
			NBThresholds:    model.Thresholds,
			LLMLabel:        p.Label,
			LLMConfidence:   p.Confidence,
			ArticleAgeHours: now.Sub(a.FetchedAt).Hours(),
			Signals: reviewpriority.Signals{
				UnknownEntityCount:             unknownEntities,
				PendingDictionaryProposalCount: pendingDictionary,
				SourceFirstSeenKnown:           sourceKnown,
				SourceFirstSeen:                firstSeen,
				HardMiningCount:                hardMining,
			},
			Now: now,
		}

		res := reviewpriority.Score(in, cfg)
		if res.Skip {
			if err := queue.Delete(ctx, p.ArticleID); err != nil {
				return result, fmt.Errorf("editorialreview: delete queue row for %s: %w", p.ArticleID, err)
			}
			result.Skipped++
			continue
		}

		entry := reviewpriority.QueueEntry{
			ArticleID:      p.ArticleID,
			ProposalID:     p.ID,
			Score:          res.Score,
			Reason:         res.Reason,
			NBZone:         in.NBZone,
			NBValue:        in.NBValue,
			LLMLabel:       p.Label,
			LLMConfidence:  p.Confidence,
			SuggestedLabel: p.Label,
		}
		if err := queue.Upsert(ctx, entry); err != nil {
			return result, fmt.Errorf("editorialreview: upsert queue row for %s: %w", p.ArticleID, err)
		}
		result.Queued++
	}
	return result, nil
}

// zoneFor mirrors editorial.Predict's zone derivation, applied directly
// to an already-computed Value (a.EditorialScore) instead of
// re-extracting features and re-running Predict.
func zoneFor(value float64, t editorial.Thresholds) editorial.Zone {
	switch {
	case value <= t.Reject:
		return editorial.ZoneReject
	case value >= t.Accept:
		return editorial.ZoneAccept
	default:
		return editorial.ZoneAmbiguous
	}
}

func loadSourceFirstSeen(ctx context.Context, repo appout.SourceRepository) (map[string]time.Time, error) {
	list, err := repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(list))
	for _, s := range list {
		if !s.FirstSeenAt.IsZero() {
			out[s.ID.String()] = s.FirstSeenAt
		}
	}
	return out, nil
}
