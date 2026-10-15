package trainmine_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/trainmine"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeArticleLister struct{ articles []article.Article }

func (f fakeArticleLister) Save(context.Context, article.Article) error { return nil }
func (f fakeArticleLister) FindByID(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

func (f fakeArticleLister) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}
func (f fakeArticleLister) Count(context.Context) (int, error) { return len(f.articles), nil }
func (f fakeArticleLister) ListCandidatesForReview(_ context.Context, _ time.Time, limit int) ([]article.Article, error) {
	if limit < len(f.articles) {
		return f.articles[:limit], nil
	}
	return f.articles, nil
}

func (f fakeArticleLister) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f fakeArticleLister) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

var _ appout.ArticleRepository = fakeArticleLister{}

type fakeTeacherLLM struct {
	labels []appout.EditorialTeacherLabel
	err    error
	calls  int
}

func (f *fakeTeacherLLM) Classify(_ context.Context, _ string, articles []appout.EditorialTeacherArticle) (appout.EditorialTeacherResult, error) {
	f.calls++
	if f.err != nil {
		return appout.EditorialTeacherResult{}, f.err
	}
	batchIDs := make(map[string]bool, len(articles))
	for _, a := range articles {
		batchIDs[a.ArticleID] = true
	}
	var batch []appout.EditorialTeacherLabel
	for _, l := range f.labels {
		if batchIDs[l.ArticleID] {
			batch = append(batch, l)
		}
	}
	return appout.EditorialTeacherResult{Labels: batch}, nil
}

var _ appout.EditorialTeacherLLM = (*fakeTeacherLLM)(nil)

type fakeHardExampleRepo struct {
	upserted []training.HardExampleCandidate
}

func (f *fakeHardExampleRepo) Upsert(_ context.Context, c training.HardExampleCandidate) error {
	f.upserted = append(f.upserted, c)
	return nil
}

func (f *fakeHardExampleRepo) UnresolvedCount(context.Context) (int, error) {
	return len(f.upserted), nil
}

func (f *fakeHardExampleRepo) ListUnresolved(context.Context, int) ([]training.HardExampleCandidate, error) {
	return f.upserted, nil
}

func (f *fakeHardExampleRepo) IsUnresolved(_ context.Context, articleID string) (bool, error) {
	for _, c := range f.upserted {
		if c.ArticleID == articleID {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeHardExampleRepo) MarkResolved(context.Context, string, time.Time) error { return nil }

var _ appout.HardExampleRepository = (*fakeHardExampleRepo)(nil)

// writeFixtureModel writes a minimal, valid promoted model artifact to
// dir via editorialmodel.Save. weights is passed straight through as
// the model's linear weights — editorial.ExtractFeatures (domain/
// editorial/editorial.go:180) turns every lowercased word token in an
// article's Title+Summary into a "uni:<word>" feature counting its
// occurrences (editorial.go:134's tokenize, editorial.go:187's "uni:"
// prefix), so a weights key like "uni:publishword" pairs with a test
// article whose Title contains the literal word "publishword".
// editorial.Predict (editorial.go:82) computes Bias + sum(weight*count)
// then squashes through a logistic onto [0,100] — thresholds carves
// that into reject/ambiguous/accept.
func writeFixtureModel(t *testing.T, dir string, weights map[string]float64, thresholds editorial.Thresholds) {
	t.Helper()
	model := editorial.Model{
		Version: "nb-v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1",
		Bias: 0, Weights: weights, Thresholds: thresholds,
	}
	manifest := editorialmodel.Manifest{
		ID: "nb-v1", Version: "nb-v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1",
		TrainedAt: time.Now().UTC(), Thresholds: thresholds, Status: "promoted",
	}
	features := make([]string, 0, len(weights))
	for f := range weights {
		features = append(features, f)
	}
	if err := editorialmodel.Save(dir, model, manifest, `{}`, editorialmodel.FeatureSchema{Version: "fs-1", Features: features}); err != nil {
		t.Fatalf("editorialmodel.Save() error = %v", err)
	}
}

func TestMine_FindsHardNegativeAndHardPositive(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model")
	writeFixtureModel(t, modelPath, map[string]float64{"uni:publishword": 8, "uni:discardword": -8}, editorial.Thresholds{Reject: 20, Accept: 80})

	articles := fakeArticleLister{articles: []article.Article{
		{ID: "hard-neg", Title: "publishword publishword publishword", Summary: ""}, // NB: accept (raw 24 -> ~100)
		{ID: "hard-pos", Title: "discardword discardword discardword", Summary: ""}, // NB: reject (raw -24 -> ~0)
		{ID: "agree", Title: "publishword publishword publishword", Summary: ""},    // NB: accept, LLM also publish -- not hard
	}}
	llm := &fakeTeacherLLM{labels: []appout.EditorialTeacherLabel{
		{ArticleID: "hard-neg", Label: "discard", Confidence: 0.95},
		{ArticleID: "hard-pos", Label: "publish", Confidence: 0.93},
		{ArticleID: "agree", Label: "publish", Confidence: 0.90},
	}}
	repo := &fakeHardExampleRepo{}

	got, err := trainmine.Mine(context.Background(), modelPath, articles, llm, repo,
		trainmine.Options{BatchLimit: 10, MinNBConfidence: 0.7, LLMAutoLabelThreshold: 0.9}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Mine() error = %v", err)
	}
	if got.ArticlesScored != 3 {
		t.Errorf("ArticlesScored = %d, want 3", got.ArticlesScored)
	}
	if got.HardNegativeCount != 1 || got.HardPositiveCount != 1 {
		t.Errorf("HardNegativeCount=%d HardPositiveCount=%d, want 1 and 1", got.HardNegativeCount, got.HardPositiveCount)
	}
	if len(repo.upserted) != 2 {
		t.Fatalf("repo.upserted = %+v, want exactly 2 rows (agree is not a hard example)", repo.upserted)
	}
}

func TestMine_NoPromotedModel_ExitsCleanly(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "does-not-exist")
	repo := &fakeHardExampleRepo{}

	got, err := trainmine.Mine(context.Background(), modelPath, fakeArticleLister{}, &fakeTeacherLLM{}, repo,
		trainmine.Options{BatchLimit: 10, MinNBConfidence: 0.7, LLMAutoLabelThreshold: 0.9}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Mine() error = %v, want nil (no promoted model is not an error)", err)
	}
	if !got.Skipped || got.SkipReason == "" {
		t.Errorf("got = %+v, want Skipped=true with a non-empty SkipReason", got)
	}
	if len(repo.upserted) != 0 {
		t.Errorf("repo.upserted = %+v, want none", repo.upserted)
	}
}

func TestMine_LLMFailureMidBatch_OnlyEarlierBatchesPersist(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model")
	writeFixtureModel(t, modelPath, map[string]float64{"uni:publishword": 8}, editorial.Thresholds{Reject: 20, Accept: 80})

	var articles []article.Article
	for i := 0; i < 3; i++ {
		articles = append(articles, article.Article{ID: fmt.Sprintf("batch1-%d", i), Title: "something else entirely"})
	}
	for i := 0; i < 2; i++ {
		articles = append(articles, article.Article{ID: fmt.Sprintf("batch2-%d", i), Title: "publishword publishword publishword"})
	}
	llm := &fakeTeacherLLM{err: errors.New("llm unavailable")}
	repo := &fakeHardExampleRepo{}

	_, err := trainmine.Mine(context.Background(), modelPath, fakeArticleLister{articles: articles}, llm, repo,
		trainmine.Options{BatchLimit: 10, MinNBConfidence: 0.7, LLMAutoLabelThreshold: 0.9, BatchSize: 3}, time.Now().UTC())
	if err == nil {
		t.Fatal("Mine() error = nil, want an error propagated from the LLM failure")
	}
	if len(repo.upserted) != 0 {
		t.Errorf("repo.upserted = %+v, want none (the one and only batch failed before any upsert)", repo.upserted)
	}
}
