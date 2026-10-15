package trainanalyze_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/trainanalyze"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

type fakeProposalRepo struct {
	pending []editoriallabelproposal.Proposal
}

func (f fakeProposalRepo) Save(ctx context.Context, p editoriallabelproposal.Proposal) (string, error) {
	return "", nil
}

func (f fakeProposalRepo) GetLatestByArticle(ctx context.Context, articleID string) (editoriallabelproposal.Proposal, bool, error) {
	return editoriallabelproposal.Proposal{}, false, nil
}

func (f fakeProposalRepo) ListLatestPerArticle(ctx context.Context) ([]editoriallabelproposal.Proposal, error) {
	return nil, nil
}

func (f fakeProposalRepo) ListByStatus(ctx context.Context, status editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error) {
	if status != editoriallabelproposal.StatusPending {
		return nil, nil
	}
	return f.pending, nil
}

func (f fakeProposalRepo) UpdateStatus(ctx context.Context, id string, status editoriallabelproposal.Status, decidedAt time.Time) error {
	return nil
}

func (f fakeProposalRepo) FilterUnprocessed(ctx context.Context, promptVersion string, articleIDs []string) ([]string, error) {
	return nil, nil
}

func (f fakeProposalRepo) MarkArticlesProcessed(ctx context.Context, promptVersion string, articleIDs []string, processedAt time.Time) error {
	return nil
}

var _ editoriallabelproposalport.EditorialLabelProposalRepository = fakeProposalRepo{}

type fakeQueueRepo struct {
	entries []reviewpriority.QueueEntry
}

func (f fakeQueueRepo) ListOrderedByScore(ctx context.Context, limit int) ([]reviewpriority.QueueEntry, error) {
	return f.entries, nil
}
func (f fakeQueueRepo) Upsert(ctx context.Context, e reviewpriority.QueueEntry) error { return nil }
func (f fakeQueueRepo) Delete(ctx context.Context, articleID string) error            { return nil }

var _ reviewqueueport.ReviewQueueRepository = fakeQueueRepo{}

func TestAnalyze(t *testing.T) {
	proposals := fakeProposalRepo{pending: []editoriallabelproposal.Proposal{
		{ArticleID: "a1", Confidence: 0.97}, // >= threshold: auto-labeled
		{ArticleID: "a2", Confidence: 0.6},  // < threshold: sent to review
		{ArticleID: "a3", Confidence: 0.98}, // auto-labeled
	}}
	queue := fakeQueueRepo{entries: []reviewpriority.QueueEntry{
		{ArticleID: "a2", Reason: "disagreement=0.50; nb_uncertainty=0.20", CreatedAt: time.Now()},
		{ArticleID: "a4", Reason: "novel_vocabulary=0.30; recency=0.10", CreatedAt: time.Now()},
		{ArticleID: "a5", Reason: "recency=0.15", CreatedAt: time.Now()},
	}}

	got, err := trainanalyze.Analyze(context.Background(), proposals, queue, 0.95, 50)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	want := trainanalyze.Result{
		ProposedCount: 3, AutoLabeledCount: 2, SentToReviewCount: 1,
		QueueSize: 3, QueueDisagreementCount: 1, QueueNovelVocabularyCount: 1, QueueOtherCount: 1,
		ManualReviewRate: 1.0 / 3.0,
	}
	if got != want {
		t.Errorf("Analyze() = %+v, want %+v", got, want)
	}
}

func TestAnalyze_ManualReviewRateIsZeroWithoutProposals(t *testing.T) {
	got, err := trainanalyze.Analyze(context.Background(), fakeProposalRepo{}, fakeQueueRepo{}, 0.95, 50)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if got.ManualReviewRate != 0 {
		t.Errorf("ManualReviewRate = %v, want 0 when nothing was analysed", got.ManualReviewRate)
	}
}

// 1,000 analysed, 50 need a human -> 5%.
func TestAnalyze_ManualReviewRate_FiftyOfThousand(t *testing.T) {
	var pending []editoriallabelproposal.Proposal
	for i := 0; i < 1000; i++ {
		c := 0.99
		if i < 50 {
			c = 0.5
		}
		pending = append(pending, editoriallabelproposal.Proposal{Confidence: c})
	}
	got, err := trainanalyze.Analyze(context.Background(), fakeProposalRepo{pending: pending}, fakeQueueRepo{}, 0.95, 50)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if got.ManualReviewRate != 0.05 {
		t.Errorf("ManualReviewRate = %v, want 0.05", got.ManualReviewRate)
	}
}
