package shadowcompare_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/shadowcompare"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// productionModel accepts "signal" articles, rejects everything else.
func productionModel() editorial.Model {
	return editorial.Model{
		Version: "nb-2026-09-01", Bias: -10, Weights: map[string]float64{"uni:signal": 20},
		Thresholds: editorial.Thresholds{Reject: 30, Accept: 70},
	}
}

// invertedCandidateModel disagrees with productionModel on every article:
// it rejects "signal" articles and accepts everything else.
func invertedCandidateModel() editorial.Model {
	return editorial.Model{
		Version: "nb-2026-09-08-candidate", Bias: 10, Weights: map[string]float64{"uni:signal": -20},
		Thresholds: editorial.Thresholds{Reject: 30, Accept: 70},
	}
}

func articleWithTitle(id, title string) article.Article {
	return article.Article{ID: id, Title: title}
}

func TestCompare_RecordsEveryDisagreement(t *testing.T) {
	articles := []article.Article{
		articleWithTitle("a1", "signal"),
		articleWithTitle("a2", "neutral text"),
	}
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	result := shadowcompare.Compare(articles, productionModel(), invertedCandidateModel(), "run-1", now)

	if result.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", result.RunID)
	}
	if result.TotalArticles != 2 {
		t.Errorf("TotalArticles = %d, want 2", result.TotalArticles)
	}
	if result.Agreements != 0 {
		t.Errorf("Agreements = %d, want 0 (inverted candidate disagrees on every article)", result.Agreements)
	}
	if len(result.Disagreements) != 2 {
		t.Fatalf("len(Disagreements) = %d, want 2", len(result.Disagreements))
	}

	byID := map[string]training.ShadowDisagreement{}
	for _, d := range result.Disagreements {
		byID[d.ArticleID] = d
	}
	a1 := byID["a1"]
	if a1.ProductionZone != string(editorial.ZoneAccept) || a1.CandidateZone != string(editorial.ZoneReject) {
		t.Errorf("a1 zones = production %q candidate %q, want accept/reject", a1.ProductionZone, a1.CandidateZone)
	}
	if a1.RunID != "run-1" || a1.ProductionVersion != "nb-2026-09-01" || a1.CandidateVersion != "nb-2026-09-08-candidate" {
		t.Errorf("a1 = %+v, want RunID/ProductionVersion/CandidateVersion populated", a1)
	}
	if !a1.CreatedAt.Equal(now) {
		t.Errorf("a1.CreatedAt = %v, want %v", a1.CreatedAt, now)
	}
}

func TestCompare_AgreementsNotRecordedAsDisagreements(t *testing.T) {
	articles := []article.Article{articleWithTitle("a1", "signal")}
	// Same model on both sides -> always agrees.
	result := shadowcompare.Compare(articles, productionModel(), productionModel(), "run-2", time.Now())

	if result.Agreements != 1 {
		t.Errorf("Agreements = %d, want 1", result.Agreements)
	}
	if len(result.Disagreements) != 0 {
		t.Errorf("len(Disagreements) = %d, want 0", len(result.Disagreements))
	}
}

type fakeArticleLister struct {
	articles []article.Article
	gotSince time.Time
	gotLimit int
}

func (f *fakeArticleLister) ListSince(_ context.Context, since time.Time, limit int) ([]article.Article, error) {
	f.gotSince = since
	f.gotLimit = limit
	return f.articles, nil
}

type fakeShadowRepo struct {
	saved []training.ShadowDisagreement
}

func (f *fakeShadowRepo) SaveDisagreements(_ context.Context, rows []training.ShadowDisagreement) error {
	f.saved = append(f.saved, rows...)
	return nil
}

func (f *fakeShadowRepo) ListByRun(_ context.Context, runID string) ([]training.ShadowDisagreement, error) {
	var out []training.ShadowDisagreement
	for _, d := range f.saved {
		if d.RunID == runID {
			out = append(out, d)
		}
	}
	return out, nil
}

func TestRun_ListsArticlesComparesAndPersistsDisagreements(t *testing.T) {
	lister := &fakeArticleLister{articles: []article.Article{articleWithTitle("a1", "signal")}}
	repo := &fakeShadowRepo{}
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

	result, err := shadowcompare.Run(context.Background(), lister, repo, productionModel(), invertedCandidateModel(),
		shadowcompare.Options{Since: since, Limit: 42, RunID: "run-3"}, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if lister.gotSince != since || lister.gotLimit != 42 {
		t.Errorf("ListSince called with since=%v limit=%d, want since=%v limit=42", lister.gotSince, lister.gotLimit, since)
	}
	if result.TotalArticles != 1 || len(result.Disagreements) != 1 {
		t.Fatalf("result = %+v, want 1 article and 1 disagreement", result)
	}
	if len(repo.saved) != 1 || repo.saved[0].ArticleID != "a1" {
		t.Errorf("repo.saved = %+v, want the a1 disagreement persisted", repo.saved)
	}
}

func TestRun_GeneratesRunIDWhenOptionsRunIDEmpty(t *testing.T) {
	lister := &fakeArticleLister{articles: nil}
	repo := &fakeShadowRepo{}

	result, err := shadowcompare.Run(context.Background(), lister, repo, productionModel(), invertedCandidateModel(),
		shadowcompare.Options{}, time.Now())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.RunID == "" {
		t.Error("RunID is empty, want a generated non-empty value")
	}
}
