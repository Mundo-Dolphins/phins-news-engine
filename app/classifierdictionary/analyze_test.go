package classifierdictionary

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/classifierproposalport"
)

type fakeArticleLister struct {
	articles []article.Article
}

func (f fakeArticleLister) ListSince(context.Context, time.Time, int) ([]article.Article, error) {
	return f.articles, nil
}

type fakeProposalRepo struct {
	upserts []classifierproposal.Proposal
	marked  []string
}

func (f *fakeProposalRepo) Upsert(_ context.Context, p classifierproposal.Proposal) (string, error) {
	f.upserts = append(f.upserts, p)
	return "p1", nil
}

func (f *fakeProposalRepo) Get(context.Context, string) (classifierproposal.Proposal, error) {
	return classifierproposal.Proposal{}, nil
}

func (f *fakeProposalRepo) List(context.Context, string, string) ([]classifierproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeProposalRepo) UpdateStatus(context.Context, string, classifierproposal.Status, time.Time) error {
	return nil
}

func (f *fakeProposalRepo) FilterUnprocessed(_ context.Context, _ string, articleIDs []string) ([]string, error) {
	return articleIDs, nil
}

func (f *fakeProposalRepo) MarkArticlesProcessed(_ context.Context, _ string, articleIDs []string, _ time.Time) error {
	f.marked = append(f.marked, articleIDs...)
	return nil
}

func (f *fakeProposalRepo) ListUnknownAggregates(context.Context, time.Time, int, int) ([]classifierproposalport.UnknownOccurrenceAggregate, error) {
	return nil, nil
}

func (f *fakeProposalRepo) Metrics(context.Context, time.Time) (classifierproposalport.Metrics, error) {
	return classifierproposalport.Metrics{}, nil
}

func TestAnalyzeCreatesPendingPositiveProposalFromRepeatedAcceptedArticles(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign Jai Rene Mossbank", Status: article.StatusPublished, FetchedAt: now.Add(-2 * time.Hour)},
		{ID: "a2", Title: "Jai Rene Mossbank joins Springfield practice", Status: article.StatusClustered, FetchedAt: now.Add(-1 * time.Hour)},
	}}
	repo := &fakeProposalRepo{}
	dict := dictionary.Dictionary{
		Categories: []dictionary.Category{{Name: "nfl_people_context", Weight: 2}},
		Decision:   dictionary.Decision{MaxCountPerTerm: 3, TitleWeightMultiplier: 1},
	}

	result, err := Analyze(context.Background(), articles, repo, dict, AnalyzeOptions{
		AnalyzerVersion: "classifier-dictionary-test", MinOccurrences: 2,
	}, now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.ArticleProposalsUpserted != 1 {
		t.Fatalf("ArticleProposalsUpserted = %d, want 1", result.ArticleProposalsUpserted)
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(repo.upserts))
	}
	got := repo.upserts[0]
	if got.Status != classifierproposal.StatusPending {
		t.Errorf("Status = %q, want pending", got.Status)
	}
	if got.Kind != classifierproposal.KindTerm || got.Category != "nfl_people_context" || got.Value != "Jai Rene Mossbank" {
		t.Errorf("proposal = %+v, want positive term in nfl_people_context", got)
	}
	if got.Occurrences != 2 || len(got.Sources) != 2 {
		t.Errorf("occurrences/sources = %d/%d, want 2/2", got.Occurrences, len(got.Sources))
	}
	if len(repo.marked) != 2 {
		t.Errorf("marked articles = %d, want 2", len(repo.marked))
	}
}

func TestExtractTitleCaseSpans_SkipsStopValues(t *testing.T) {
	if got := extractTitleCaseSpans("Super Bowl odds for Jordan Sample"); len(got) != 1 || got[0] != "Jordan Sample" {
		t.Fatalf("default spans = %v, want only Jordan Sample", got)
	}
	if got := extractTitleCaseSpans("Visit Capital City now"); len(got) != 1 || got[0] != "Visit Capital City" {
		t.Fatalf("spans = %v, want the city span when it is not a stop value", got)
	}
	StopValues["Visit Capital City"] = true
	t.Cleanup(func() { delete(StopValues, "Visit Capital City") })
	if got := extractTitleCaseSpans("Visit Capital City now"); len(got) != 0 {
		t.Fatalf("spans = %v, want a host-added stop value skipped", got)
	}
}
