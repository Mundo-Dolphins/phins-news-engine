package editorialteacher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ArticleTeacherLister is the read access Classify needs over stored
// articles — satisfied directly by the existing concrete
// *sqlite.ArticleRepository.ListSince, same pattern as
// entitydiscovery.ArticleLister.
type ArticleTeacherLister interface {
	ListSince(ctx context.Context, since time.Time, limit int) ([]article.Article, error)
}

// ClassifyOptions configures one `editorial classify` run.
type ClassifyOptions struct {
	// Since is the lower bound on article fetched_at; the zero value
	// means no lower bound.
	Since time.Time
	// BatchSize is how many articles are sent to the LLM per request.
	BatchSize int
	// Limit bounds how many articles ListSince returns in total for
	// this run.
	Limit int
	// Model tags every proposal this run produces with the model name
	// used to produce it.
	Model string
	// Force, when true, reprocesses articles even if they're already
	// recorded as processed for PromptVersion.
	Force bool
}

// ClassifyResult summarizes one run for the CLI to print.
type ClassifyResult struct {
	ArticlesConsidered int
	BatchesTotal       int
	BatchesFailed      int
	ProposalsSaved     int
	// ProposalsSkipped counts articles whose result was valid but
	// skipped because the article already had a pending proposal
	// (editoriallabelproposal.ErrPendingProposalExists) — the
	// review flow must resolve that proposal first.
	ProposalsSkipped int
	// LabelsMissing counts articles in a batch for which the LLM's
	// response contained no label at all (distinct from a label that
	// was present but failed ValidateLabel) — a realistic failure mode
	// for small local models that silently truncate their output.
	LabelsMissing int
	// BatchErrors records batch-level failures (LLM/HTTP call itself
	// failed) — the whole batch is skipped and its articles are left
	// unmarked for retry.
	BatchErrors []string
	// LabelErrors records per-label ValidateLabel failures within an
	// otherwise-successful batch — the batch itself is not considered
	// failed.
	LabelErrors []string
}

// Classify reads unprocessed articles since opts.Since (bounded by
// opts.Limit), sends them to llm in batches of opts.BatchSize alongside
// the full publication policy text, validates each returned label, and
// saves every valid one as a pending editoriallabelproposal.Proposal via
// repo. A batch that errors (LLM unreachable, invalid JSON) is counted
// in BatchesFailed and skipped — its articles are left unmarked so a
// later run retries them — but never aborts the rest of the run.
// Classify itself returns a non-nil error only for an infrastructure
// failure reading articles, checking/marking processed state, or saving
// a proposal for a reason other than
// editoriallabelproposal.ErrPendingProposalExists (which is expected
// and counted in ProposalsSkipped, not an error).
func Classify(ctx context.Context, articles ArticleTeacherLister, llm appout.EditorialTeacherLLM, repo editoriallabelproposalport.EditorialLabelProposalRepository, opts ClassifyOptions, now time.Time) (ClassifyResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	all, err := articles.ListSince(ctx, opts.Since, limit)
	if err != nil {
		return ClassifyResult{}, fmt.Errorf("editorialteacher: list articles: %w", err)
	}

	pending := all
	if !opts.Force {
		ids := make([]string, len(all))
		for i, a := range all {
			ids[i] = a.ID
		}
		unprocessedIDs, err := repo.FilterUnprocessed(ctx, PromptVersion, ids)
		if err != nil {
			return ClassifyResult{}, fmt.Errorf("editorialteacher: filter unprocessed articles: %w", err)
		}
		unprocessedSet := make(map[string]bool, len(unprocessedIDs))
		for _, id := range unprocessedIDs {
			unprocessedSet[id] = true
		}
		pending = pending[:0]
		for _, a := range all {
			if unprocessedSet[a.ID] {
				pending = append(pending, a)
			}
		}
	}

	result := ClassifyResult{ArticlesConsidered: len(pending)}

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	for start := 0; start < len(pending); start += batchSize {
		end := start + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		result.BatchesTotal++

		teacherArticles := make([]appout.EditorialTeacherArticle, len(batch))
		batchIDs := make(map[string]bool, len(batch))
		for i, a := range batch {
			teacherArticles[i] = BuildTeacherArticle(a)
			batchIDs[a.ID] = true
		}

		res, err := llm.Classify(ctx, PublicationPolicy, teacherArticles)
		if err != nil {
			result.BatchesFailed++
			result.BatchErrors = append(result.BatchErrors, err.Error())
			continue
		}

		labeledIDs := make(map[string]bool, len(res.Labels))
		handledIDs := make(map[string]bool, len(res.Labels))
		for _, l := range res.Labels {
			labeledIDs[l.ArticleID] = true
			if err := ValidateLabel(l, batchIDs); err != nil {
				result.LabelErrors = append(result.LabelErrors, err.Error())
				continue
			}
			p := editoriallabelproposal.Proposal{
				ArticleID:     l.ArticleID,
				Label:         l.Label,
				Confidence:    l.Confidence,
				Reason:        l.Reason,
				Model:         opts.Model,
				PromptVersion: PromptVersion,
				PolicyVersion: PolicyVersion,
				Status:        editoriallabelproposal.StatusPending,
				CreatedAt:     now,
			}
			if _, err := repo.Save(ctx, p); err != nil {
				if isPendingProposalExists(err) {
					result.ProposalsSkipped++
					handledIDs[l.ArticleID] = true
					continue
				}
				return result, fmt.Errorf("editorialteacher: save proposal for %s: %w", l.ArticleID, err)
			}
			result.ProposalsSaved++
			handledIDs[l.ArticleID] = true
		}

		for _, a := range batch {
			if !labeledIDs[a.ID] {
				result.LabelsMissing++
			}
		}

		articleIDs := make([]string, 0, len(handledIDs))
		for _, a := range batch {
			if handledIDs[a.ID] {
				articleIDs = append(articleIDs, a.ID)
			}
		}
		if len(articleIDs) > 0 {
			if err := repo.MarkArticlesProcessed(ctx, PromptVersion, articleIDs, now); err != nil {
				return result, fmt.Errorf("editorialteacher: mark articles processed: %w", err)
			}
		}
	}

	return result, nil
}

func isPendingProposalExists(err error) bool {
	return errors.Is(err, editoriallabelproposal.ErrPendingProposalExists)
}
