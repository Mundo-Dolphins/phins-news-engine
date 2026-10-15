package editorialtrain_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// fakeProposalRepo backs editoriallabelproposalport.
// EditorialLabelProposalRepository for FetchSilverExamples' tests --
// only ListLatestPerArticle's behavior matters here, so every other
// method is a no-op.
type fakeProposalRepo struct {
	latest []editoriallabelproposal.Proposal
}

func (f *fakeProposalRepo) Save(context.Context, editoriallabelproposal.Proposal) (string, error) {
	return "", nil
}

func (f *fakeProposalRepo) GetLatestByArticle(context.Context, string) (editoriallabelproposal.Proposal, bool, error) {
	return editoriallabelproposal.Proposal{}, false, nil
}

func (f *fakeProposalRepo) ListByStatus(context.Context, editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeProposalRepo) UpdateStatus(context.Context, string, editoriallabelproposal.Status, time.Time) error {
	return nil
}

func (f *fakeProposalRepo) FilterUnprocessed(context.Context, string, []string) ([]string, error) {
	return nil, nil
}

func (f *fakeProposalRepo) MarkArticlesProcessed(context.Context, string, []string, time.Time) error {
	return nil
}

func (f *fakeProposalRepo) ListLatestPerArticle(context.Context) ([]editoriallabelproposal.Proposal, error) {
	return f.latest, nil
}

// fakeArticleRepo backs out.ArticleRepository for FetchSilverExamples'
// tests -- only FindByID's behavior matters here, so every other
// method is a no-op. A missing article returns a wrapped
// sql.ErrNoRows, mirroring the real sqlite adapter.
type fakeArticleRepo struct {
	byID map[string]article.Article
}

func (f *fakeArticleRepo) Save(context.Context, article.Article) error { return nil }
func (f *fakeArticleRepo) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f.byID[id]
	if !ok {
		return article.Article{}, fmt.Errorf("fakeArticleRepo: find article %s: %w", id, sql.ErrNoRows)
	}
	return a, nil
}

func (f *fakeArticleRepo) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeArticleRepo) Count(context.Context) (int, error) { return 0, nil }
func (f *fakeArticleRepo) ListCandidatesForReview(context.Context, time.Time, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticleRepo) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticleRepo) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

var _ out.ArticleRepository = (*fakeArticleRepo)(nil)

func TestFetchSilverExamples_IncludesConfidentUnlabeledProposal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	proposals := &fakeProposalRepo{latest: []editoriallabelproposal.Proposal{
		{ArticleID: "a1", Label: "publish", Confidence: 0.97, Reason: "r1", CreatedAt: now},
	}}
	articles := &fakeArticleRepo{byID: map[string]article.Article{
		"a1": {ID: "a1", Title: "T1", Summary: "B1", Domain: "d.com", PublishedAt: now, FetchedAt: now},
	}}

	examples, skipped, err := editorialtrain.FetchSilverExamples(ctx, proposals, &fakeLabelRepo{}, articles, nil, 0.9)
	if err != nil {
		t.Fatalf("FetchSilverExamples() error = %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(examples) != 1 || examples[0].ArticleID != "a1" || examples[0].Tier != "silver" || examples[0].Label != "publish" {
		t.Errorf("examples = %+v, want one silver publish example for a1", examples)
	}
}

func TestFetchSilverExamples_MapsDiscardToReject(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	proposals := &fakeProposalRepo{latest: []editoriallabelproposal.Proposal{
		{ArticleID: "a1", Label: "discard", Confidence: 0.97, CreatedAt: now},
	}}
	articles := &fakeArticleRepo{byID: map[string]article.Article{"a1": {ID: "a1"}}}

	examples, _, err := editorialtrain.FetchSilverExamples(ctx, proposals, &fakeLabelRepo{}, articles, nil, 0.9)
	if err != nil {
		t.Fatalf("FetchSilverExamples() error = %v", err)
	}
	if len(examples) != 1 || examples[0].Label != "reject" {
		t.Errorf("examples = %+v, want one reject example (discard maps to reject)", examples)
	}
}

func TestFetchSilverExamples_ExcludesBelowThreshold(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	proposals := &fakeProposalRepo{latest: []editoriallabelproposal.Proposal{
		{ArticleID: "a1", Label: "publish", Confidence: 0.5, CreatedAt: now},
	}}
	articles := &fakeArticleRepo{byID: map[string]article.Article{"a1": {ID: "a1"}}}

	examples, _, err := editorialtrain.FetchSilverExamples(ctx, proposals, &fakeLabelRepo{}, articles, nil, 0.9)
	if err != nil {
		t.Fatalf("FetchSilverExamples() error = %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("examples = %+v, want none (confidence below threshold)", examples)
	}
}

func TestFetchSilverExamples_ExcludesArticlesWithActiveHumanLabel(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	proposals := &fakeProposalRepo{latest: []editoriallabelproposal.Proposal{
		{ArticleID: "a1", Label: "publish", Confidence: 0.97, CreatedAt: now},
	}}
	labels := &fakeLabelRepo{activeLabels: []training.Example{{ArticleID: "a1", Label: "reject"}}}
	articles := &fakeArticleRepo{byID: map[string]article.Article{"a1": {ID: "a1"}}}

	examples, _, err := editorialtrain.FetchSilverExamples(ctx, proposals, labels, articles, nil, 0.9)
	if err != nil {
		t.Fatalf("FetchSilverExamples() error = %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("examples = %+v, want none (a1 already has a Gold human label)", examples)
	}
}

func TestFetchSilverExamples_ExcludesUncertainEvenAtHighConfidence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	proposals := &fakeProposalRepo{latest: []editoriallabelproposal.Proposal{
		{ArticleID: "a1", Label: "uncertain", Confidence: 0.99, CreatedAt: now},
	}}
	articles := &fakeArticleRepo{byID: map[string]article.Article{"a1": {ID: "a1"}}}

	examples, _, err := editorialtrain.FetchSilverExamples(ctx, proposals, &fakeLabelRepo{}, articles, nil, 0.9)
	if err != nil {
		t.Fatalf("FetchSilverExamples() error = %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("examples = %+v, want none (uncertain label excluded regardless of confidence)", examples)
	}
}

func TestFetchSilverExamples_SkipsUnrecognizedLabelWithoutError(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	proposals := &fakeProposalRepo{latest: []editoriallabelproposal.Proposal{
		// "maybe" is not a value editoriallabelproposal.Proposal.Label's
		// vocabulary currently produces (only "publish"/"discard"/
		// "uncertain" — and "uncertain" is filtered before this point),
		// but ComputeTier only looks at Confidence and human-label
		// status, so a proposal with this invented label can still reach
		// the label-mapping switch. It must be skipped, not trained as
		// "reject".
		{ArticleID: "a1", Label: "maybe", Confidence: 0.97, CreatedAt: now},
	}}
	articles := &fakeArticleRepo{byID: map[string]article.Article{"a1": {ID: "a1"}}}

	examples, skipped, err := editorialtrain.FetchSilverExamples(ctx, proposals, &fakeLabelRepo{}, articles, nil, 0.9)
	if err != nil {
		t.Fatalf("FetchSilverExamples() error = %v, want nil (unrecognized label skips the candidate, does not error the export)", err)
	}
	if len(examples) != 0 {
		t.Errorf("examples = %+v, want none (unrecognized label %q must not silently train as reject)", examples, "maybe")
	}
	if skipped != 0 {
		t.Errorf("skipped (missing-article count) = %d, want 0 -- an unrecognized label is not a missing-article skip", skipped)
	}
}

func TestFetchSilverExamples_SkipsMissingArticleWithoutError(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	proposals := &fakeProposalRepo{latest: []editoriallabelproposal.Proposal{
		{ArticleID: "a1", Label: "discard", Confidence: 0.95, CreatedAt: now},
	}}
	articles := &fakeArticleRepo{byID: map[string]article.Article{}}

	examples, skipped, err := editorialtrain.FetchSilverExamples(ctx, proposals, &fakeLabelRepo{}, articles, nil, 0.9)
	if err != nil {
		t.Fatalf("FetchSilverExamples() error = %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("examples = %+v, want none (article missing)", examples)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}
