package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// fakeArticleRepoForSimulate implements just the out.ArticleRepository
// method SimulateSourcePolicy needs (FindByID), returning a fixed article.
type fakeArticleRepoForSimulate struct {
	article article.Article
}

func (f *fakeArticleRepoForSimulate) Save(context.Context, article.Article) error {
	return nil
}

func (f *fakeArticleRepoForSimulate) FindByID(context.Context, string) (article.Article, error) {
	return f.article, nil
}

func (f *fakeArticleRepoForSimulate) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}

func (f *fakeArticleRepoForSimulate) Count(context.Context) (int, error) {
	return 0, nil
}

func (f *fakeArticleRepoForSimulate) ListCandidatesForReview(context.Context, time.Time, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticleRepoForSimulate) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticleRepoForSimulate) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

func TestSimulateSourcePolicy_ReturnsRecordedDecisions(t *testing.T) {
	articles := &fakeArticleRepoForSimulate{article: article.Article{ID: "a2", Source: source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS}}}
	repo := &fakeSourcePolicyAuditRepo{}
	repo.byArticle = []out.SourcePolicyDecisionRecord{
		{ID: "1", ArticleID: "a2", SourceID: "sprisotopes", DecisionCode: "official_confirmation_no_publish", DecidedAt: time.Now()},
	}
	sim, err := usecase.SimulateSourcePolicy(context.Background(), repo, articles, "a2")
	if err != nil {
		t.Fatalf("SimulateSourcePolicy() error = %v", err)
	}
	if sim.ArticleID != "a2" || len(sim.Decisions) != 1 {
		t.Errorf("sim = %+v, want ArticleID a2 with 1 decision", sim)
	}
}
