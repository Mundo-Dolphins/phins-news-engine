package editorialteacher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeArticleLister struct {
	articles []article.Article
}

func (f fakeArticleLister) ListSince(ctx context.Context, since time.Time, limit int) ([]article.Article, error) {
	return f.articles, nil
}

type fakeTeacherLLM struct {
	// resultsByArticleID maps an article id to the label this fake
	// returns for it; an id with no entry is omitted from the response.
	resultsByArticleID map[string]appout.EditorialTeacherLabel
	err                error
	calls              int
}

func (f *fakeTeacherLLM) Classify(ctx context.Context, policyText string, articles []appout.EditorialTeacherArticle) (appout.EditorialTeacherResult, error) {
	f.calls++
	if f.err != nil {
		return appout.EditorialTeacherResult{}, f.err
	}
	var labels []appout.EditorialTeacherLabel
	for _, a := range articles {
		if l, ok := f.resultsByArticleID[a.ArticleID]; ok {
			labels = append(labels, l)
		}
	}
	return appout.EditorialTeacherResult{Labels: labels}, nil
}

type fakeProposalRepo struct {
	saved      []editoriallabelproposal.Proposal
	processed  map[string]bool // "promptVersion|articleID"
	saveErr    error
	pendingIDs map[string]bool // article ids that should return ErrPendingProposalExists on Save
}

func newFakeProposalRepo() *fakeProposalRepo {
	return &fakeProposalRepo{processed: map[string]bool{}, pendingIDs: map[string]bool{}}
}

func (f *fakeProposalRepo) Save(ctx context.Context, p editoriallabelproposal.Proposal) (string, error) {
	if f.pendingIDs[p.ArticleID] {
		return "", editoriallabelproposal.ErrPendingProposalExists
	}
	if f.saveErr != nil {
		return "", f.saveErr
	}
	f.saved = append(f.saved, p)
	return "id-" + p.ArticleID, nil
}

func (f *fakeProposalRepo) GetLatestByArticle(ctx context.Context, articleID string) (editoriallabelproposal.Proposal, bool, error) {
	return editoriallabelproposal.Proposal{}, false, nil
}

func (f *fakeProposalRepo) ListLatestPerArticle(ctx context.Context) ([]editoriallabelproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeProposalRepo) ListByStatus(ctx context.Context, status editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeProposalRepo) UpdateStatus(ctx context.Context, id string, status editoriallabelproposal.Status, decidedAt time.Time) error {
	return nil
}

func (f *fakeProposalRepo) FilterUnprocessed(ctx context.Context, promptVersion string, articleIDs []string) ([]string, error) {
	var out []string
	for _, id := range articleIDs {
		if !f.processed[promptVersion+"|"+id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeProposalRepo) MarkArticlesProcessed(ctx context.Context, promptVersion string, articleIDs []string, processedAt time.Time) error {
	for _, id := range articleIDs {
		f.processed[promptVersion+"|"+id] = true
	}
	return nil
}

func TestClassify_SavesValidProposals(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "T1", Summary: "S1"},
		{ID: "a2", Title: "T2", Summary: "S2"},
	}}
	llm := &fakeTeacherLLM{resultsByArticleID: map[string]appout.EditorialTeacherLabel{
		"a1": {ArticleID: "a1", Label: "publish", Confidence: 0.9, Reason: "on-topic"},
		"a2": {ArticleID: "a2", Label: "discard", Confidence: 0.2, Reason: "off-topic"},
	}}
	repo := newFakeProposalRepo()

	result, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 10, Limit: 10, Model: "qwen3:4b"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if result.ArticlesConsidered != 2 || result.ProposalsSaved != 2 || result.BatchesFailed != 0 {
		t.Errorf("result = %+v, want ArticlesConsidered=2 ProposalsSaved=2 BatchesFailed=0", result)
	}
	if len(repo.saved) != 2 {
		t.Fatalf("saved = %d proposals, want 2", len(repo.saved))
	}
	for _, p := range repo.saved {
		if p.PromptVersion != PromptVersion || p.PolicyVersion != PolicyVersion || p.Model != "qwen3:4b" || p.Status != editoriallabelproposal.StatusPending {
			t.Errorf("proposal %+v missing expected metadata", p)
		}
	}
}

func TestClassify_BatchFailureDoesNotAbortRun(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "T1"}, {ID: "a2", Title: "T2"}, {ID: "a3", Title: "T3"},
	}}
	llm := &fakeTeacherLLM{err: errors.New("connection refused")}
	repo := newFakeProposalRepo()

	result, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 1, Limit: 10}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Classify() error = %v, want nil (batch failures must not be a hard error)", err)
	}
	if result.BatchesTotal != 3 || result.BatchesFailed != 3 || result.ProposalsSaved != 0 {
		t.Errorf("result = %+v, want BatchesTotal=3 BatchesFailed=3 ProposalsSaved=0", result)
	}
	if len(result.BatchErrors) != 3 {
		t.Errorf("BatchErrors = %v, want 3 entries", result.BatchErrors)
	}
	if len(repo.processed) != 0 {
		t.Errorf("repo.processed = %v, want empty (failed batches must leave articles unmarked for retry)", repo.processed)
	}
}

func TestClassify_MissingLabelsAreNotMarkedProcessed(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "T1"}, {ID: "a2", Title: "T2"},
	}}
	// The fake LLM only returns a label for a1 — a2 is silently omitted,
	// simulating a small local model truncating its output.
	llm := &fakeTeacherLLM{resultsByArticleID: map[string]appout.EditorialTeacherLabel{
		"a1": {ArticleID: "a1", Label: "publish", Confidence: 0.9},
	}}
	repo := newFakeProposalRepo()

	result, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 10, Limit: 10}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if result.LabelsMissing != 1 {
		t.Errorf("LabelsMissing = %d, want 1", result.LabelsMissing)
	}
	if result.ProposalsSaved != 1 {
		t.Errorf("ProposalsSaved = %d, want 1", result.ProposalsSaved)
	}
	if repo.processed[PromptVersion+"|a2"] {
		t.Errorf("a2 (no label returned) was marked processed, want left unmarked for retry")
	}
	if !repo.processed[PromptVersion+"|a1"] {
		t.Errorf("a1 (labeled and saved) was not marked processed, want marked")
	}
}

func TestClassify_HardSaveErrorAbortsRun(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{{ID: "a1", Title: "T1"}}}
	llm := &fakeTeacherLLM{resultsByArticleID: map[string]appout.EditorialTeacherLabel{
		"a1": {ArticleID: "a1", Label: "publish", Confidence: 0.9},
	}}
	repo := newFakeProposalRepo()
	repo.saveErr = errors.New("disk full")

	_, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 10, Limit: 10}, time.Now().UTC())
	if err == nil {
		t.Fatal("Classify() error = nil, want non-nil for a hard Save error")
	}
}

func TestClassify_InvalidLabelSkippedWithoutFailingBatch(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "T1"}, {ID: "a2", Title: "T2"},
	}}
	llm := &fakeTeacherLLM{resultsByArticleID: map[string]appout.EditorialTeacherLabel{
		"a1": {ArticleID: "a1", Label: "not-a-real-label", Confidence: 0.9},
		"a2": {ArticleID: "a2", Label: "publish", Confidence: 0.9},
	}}
	repo := newFakeProposalRepo()

	result, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 10, Limit: 10}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if result.BatchesFailed != 0 || result.ProposalsSaved != 1 {
		t.Errorf("result = %+v, want BatchesFailed=0 ProposalsSaved=1", result)
	}
}

func TestClassify_PendingProposalExistsCountsAsSkipped(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{{ID: "a1", Title: "T1"}}}
	llm := &fakeTeacherLLM{resultsByArticleID: map[string]appout.EditorialTeacherLabel{
		"a1": {ArticleID: "a1", Label: "publish", Confidence: 0.9},
	}}
	repo := newFakeProposalRepo()
	repo.pendingIDs["a1"] = true

	result, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 10, Limit: 10}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if result.ProposalsSkipped != 1 || result.ProposalsSaved != 0 {
		t.Errorf("result = %+v, want ProposalsSkipped=1 ProposalsSaved=0", result)
	}
}

func TestClassify_RespectsFilterUnprocessed(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{{ID: "a1", Title: "T1"}}}
	llm := &fakeTeacherLLM{resultsByArticleID: map[string]appout.EditorialTeacherLabel{
		"a1": {ArticleID: "a1", Label: "publish", Confidence: 0.9},
	}}
	repo := newFakeProposalRepo()
	repo.processed[PromptVersion+"|a1"] = true

	result, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 10, Limit: 10}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if result.ArticlesConsidered != 0 || llm.calls != 0 {
		t.Errorf("result = %+v, llm.calls = %d, want ArticlesConsidered=0 and no LLM call", result, llm.calls)
	}
}

func TestClassify_ForceReprocessesAlreadyMarkedArticles(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{{ID: "a1", Title: "T1"}}}
	llm := &fakeTeacherLLM{resultsByArticleID: map[string]appout.EditorialTeacherLabel{
		"a1": {ArticleID: "a1", Label: "publish", Confidence: 0.9},
	}}
	repo := newFakeProposalRepo()
	repo.processed[PromptVersion+"|a1"] = true

	result, err := Classify(context.Background(), articles, llm, repo, ClassifyOptions{BatchSize: 10, Limit: 10, Force: true}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if result.ArticlesConsidered != 1 || result.ProposalsSaved != 1 {
		t.Errorf("result = %+v, want ArticlesConsidered=1 ProposalsSaved=1 with Force", result)
	}
}
