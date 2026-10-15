// Package shadowcompare runs an offline comparison between the production
// editorial model and a candidate model against the same recent stored
// articles, without touching live traffic. Every
// article where the two models' Zone disagrees is persisted for a human
// to inspect; agreements are only counted, never stored — see the
// migration doc comment for why.
package shadowcompare

import (
	"context"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ArticleLister is the read access Run needs over stored articles —
// satisfied directly by the existing concrete *sqlite.ArticleRepository.
// ListSince, same pattern as editorialteacher.ArticleTeacherLister.
type ArticleLister interface {
	ListSince(ctx context.Context, since time.Time, limit int) ([]article.Article, error)
}

// Options configures one `train shadow-compare` run.
type Options struct {
	// Since is the lower bound on article fetched_at; the zero value
	// means no lower bound.
	Since time.Time
	// Limit bounds how many articles ListSince returns in total for this
	// run. <= 0 means Run applies its own default (see Run).
	Limit int
	// RunID identifies this run's rows in editorial_shadow_comparisons.
	// Empty means Run generates one from now.
	RunID string
}

// Result summarizes one shadow-comparison run.
type Result struct {
	RunID         string
	TotalArticles int
	Agreements    int
	Disagreements []training.ShadowDisagreement
}

// Compare scores every article in articles against both production and
// candidate (via editorial.Evaluate, using each article's own
// already-recorded IsTrustedSource flag), and records one
// training.ShadowDisagreement for every article where the two models'
// Zone differs. Pure and deterministic given now — no I/O.
func Compare(articles []article.Article, production, candidate editorial.Model, runID string, now time.Time) Result {
	result := Result{RunID: runID, TotalArticles: len(articles)}
	for _, a := range articles {
		prodScore := editorial.Evaluate(a, a.IsTrustedSource, &production)
		candScore := editorial.Evaluate(a, a.IsTrustedSource, &candidate)

		if prodScore.Zone == candScore.Zone {
			result.Agreements++
			continue
		}

		result.Disagreements = append(result.Disagreements, training.ShadowDisagreement{
			RunID: runID, ArticleID: a.ID, Title: a.Title,
			ProductionVersion: production.Version, ProductionZone: string(prodScore.Zone), ProductionScore: prodScore.Value,
			CandidateVersion: candidate.Version, CandidateZone: string(candScore.Zone), CandidateScore: candScore.Value,
			CreatedAt: now,
		})
	}
	return result
}

// Run lists recent articles via articles.ListSince (bounded by
// opts.Limit, default 5000), compares production against candidate via
// Compare, and persists every disagreement via repo.SaveDisagreements.
// Returns a non-nil error only for an infrastructure failure listing
// articles or saving disagreements.
func Run(ctx context.Context, articles ArticleLister, repo appout.ShadowComparisonRepository, production, candidate editorial.Model, opts Options, now time.Time) (Result, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 5000
	}
	runID := opts.RunID
	if runID == "" {
		runID = fmt.Sprintf("shadow-%d", now.UnixNano())
	}

	all, err := articles.ListSince(ctx, opts.Since, limit)
	if err != nil {
		return Result{}, fmt.Errorf("shadowcompare: list articles: %w", err)
	}

	result := Compare(all, production, candidate, runID, now)

	if len(result.Disagreements) > 0 {
		if err := repo.SaveDisagreements(ctx, result.Disagreements); err != nil {
			return Result{}, fmt.Errorf("shadowcompare: save disagreements: %w", err)
		}
	}

	return result, nil
}
