package editorialreview_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialreview"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var _ appout.SourceRepository = (*fakeSourceRepo)(nil)

type fakeProposalRepo struct {
	pending []editoriallabelproposal.Proposal
}

func (f *fakeProposalRepo) Save(context.Context, editoriallabelproposal.Proposal) (string, error) {
	return "", nil
}

func (f *fakeProposalRepo) GetLatestByArticle(context.Context, string) (editoriallabelproposal.Proposal, bool, error) {
	return editoriallabelproposal.Proposal{}, false, nil
}

func (f *fakeProposalRepo) ListLatestPerArticle(context.Context) ([]editoriallabelproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeProposalRepo) ListByStatus(_ context.Context, status editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error) {
	if status != editoriallabelproposal.StatusPending {
		return nil, nil
	}
	return f.pending, nil
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

type fakeArticleGetter map[string]article.Article

// FindByID returns a wrapped article.ErrNotFound for any id not present
// in the map, simulating an article deleted by a retention purge after
// its proposal was created.
func (f fakeArticleGetter) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f[id]
	if !ok {
		return article.Article{}, fmt.Errorf("fakeArticleGetter: %s: %w", id, article.ErrNotFound)
	}
	return a, nil
}

type fakeSourceRepo struct{ sources []source.Source }

func (f *fakeSourceRepo) Save(context.Context, source.Source) error     { return nil }
func (f *fakeSourceRepo) List(context.Context) ([]source.Source, error) { return f.sources, nil }

type fakeSignalsRepo struct {
	unknownEntities map[string]int
	pendingDict     map[string]int
}

func (f *fakeSignalsRepo) CountUnknownEntities(_ context.Context, articleID string) (int, error) {
	return f.unknownEntities[articleID], nil
}

func (f *fakeSignalsRepo) CountPendingDictionaryProposals(_ context.Context, articleID string) (int, error) {
	return f.pendingDict[articleID], nil
}

type fakeQueueRepo struct {
	upserted map[string]reviewpriority.QueueEntry
	deleted  map[string]bool
}

func newFakeQueueRepo() *fakeQueueRepo {
	return &fakeQueueRepo{upserted: map[string]reviewpriority.QueueEntry{}, deleted: map[string]bool{}}
}

func (f *fakeQueueRepo) Upsert(_ context.Context, e reviewpriority.QueueEntry) error {
	f.upserted[e.ArticleID] = e
	return nil
}

func (f *fakeQueueRepo) Delete(_ context.Context, articleID string) error {
	f.deleted[articleID] = true
	return nil
}

func (f *fakeQueueRepo) ListOrderedByScore(context.Context, int) ([]reviewpriority.QueueEntry, error) {
	return nil, nil
}

// fakeHardExampleRepo answers IsUnresolved from a set of article IDs;
// every other method is unused by Sync.
type fakeHardExampleRepo struct{ unresolved map[string]bool }

func (f *fakeHardExampleRepo) Upsert(context.Context, training.HardExampleCandidate) error {
	return nil
}

func (f *fakeHardExampleRepo) UnresolvedCount(context.Context) (int, error) {
	return len(f.unresolved), nil
}

func (f *fakeHardExampleRepo) ListUnresolved(context.Context, int) ([]training.HardExampleCandidate, error) {
	return nil, nil
}

func (f *fakeHardExampleRepo) IsUnresolved(_ context.Context, articleID string) (bool, error) {
	return f.unresolved[articleID], nil
}
func (f *fakeHardExampleRepo) MarkResolved(context.Context, string, time.Time) error { return nil }

var _ appout.HardExampleRepository = (*fakeHardExampleRepo)(nil)

func testModel() editorial.Model {
	return editorial.Model{Thresholds: editorial.Thresholds{Reject: 20, Accept: 80}}
}

func testScoringConfig() reviewpriority.Config {
	return reviewpriority.Config{
		Weights:                         reviewpriority.Weights{Disagreement: 1, NBUncertainty: 0.7, LLMUncertainty: 0.7, NovelVocabulary: 0.5, UnknownEntities: 0.5, NewSource: 0.3, Recency: 0.2, HardMining: 0.6},
		LLMConfidenceAutoLabelThreshold: 0.9,
		LLMConfidenceUncertainThreshold: 0.5,
		NewSourceWindowHours:            72,
		RecencyWindowHours:              24,
	}
}

func TestSync_DisagreementIsQueued(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	proposals := &fakeProposalRepo{pending: []editoriallabelproposal.Proposal{
		{ID: "proposal-1", ArticleID: "article-1", Label: "discard", Confidence: 0.7},
	}}
	articles := fakeArticleGetter{
		"article-1": article.Article{ID: "article-1", EditorialScore: 90, ClassifierVersion: "test-model-v1", FetchedAt: now.Add(-1 * time.Hour)},
	}
	sources := &fakeSourceRepo{}
	signals := &fakeSignalsRepo{unknownEntities: map[string]int{}, pendingDict: map[string]int{}}
	queue := newFakeQueueRepo()
	hardExamples := &fakeHardExampleRepo{}

	result, err := editorialreview.Sync(ctx, proposals, articles, sources, signals, queue, hardExamples, testModel(), testScoringConfig(), now)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Queued != 1 || result.Skipped != 0 {
		t.Errorf("result = %+v, want Queued=1, Skipped=0", result)
	}
	entry, ok := queue.upserted["article-1"]
	if !ok {
		t.Fatalf("article-1 was not upserted into the queue")
	}
	if entry.NBZone != editorial.ZoneAccept || entry.LLMLabel != "discard" {
		t.Errorf("entry = %+v, want NBZone=accept, LLMLabel=discard", entry)
	}
	if entry.ProposalID != "proposal-1" {
		t.Errorf("entry.ProposalID = %q, want %q", entry.ProposalID, "proposal-1")
	}
}

func TestSync_ObviousAgreementIsDeletedNotQueued(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	proposals := &fakeProposalRepo{pending: []editoriallabelproposal.Proposal{
		{ID: "proposal-1", ArticleID: "article-1", Label: "publish", Confidence: 0.99},
	}}
	articles := fakeArticleGetter{
		"article-1": article.Article{ID: "article-1", EditorialScore: 95, ClassifierVersion: "test-model-v1", FetchedAt: now},
	}
	sources := &fakeSourceRepo{}
	signals := &fakeSignalsRepo{unknownEntities: map[string]int{}, pendingDict: map[string]int{}}
	queue := newFakeQueueRepo()
	hardExamples := &fakeHardExampleRepo{}

	result, err := editorialreview.Sync(ctx, proposals, articles, sources, signals, queue, hardExamples, testModel(), testScoringConfig(), now)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Skipped != 1 || result.Queued != 0 {
		t.Errorf("result = %+v, want Queued=0, Skipped=1", result)
	}
	if !queue.deleted["article-1"] {
		t.Errorf("article-1 should have had Delete called (obvious agreement)")
	}
	if _, ok := queue.upserted["article-1"]; ok {
		t.Errorf("article-1 should never have been upserted")
	}
}

// TestSync_OrphanedArticleDoesNotAbort covers Finding 1 (final review):
// a proposal whose article was deleted by a retention purge must be
// dropped, not treated as a fatal error that would abort every future
// Sync run.
func TestSync_OrphanedArticleDoesNotAbort(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	proposals := &fakeProposalRepo{pending: []editoriallabelproposal.Proposal{
		{ID: "proposal-orphan", ArticleID: "article-purged", Label: "discard", Confidence: 0.7},
		{ID: "proposal-2", ArticleID: "article-2", Label: "discard", Confidence: 0.7},
	}}
	// article-purged is intentionally absent from the map: fakeArticleGetter
	// returns a wrapped article.ErrNotFound for any id it doesn't hold,
	// simulating the retention purge.
	articles := fakeArticleGetter{
		"article-2": article.Article{ID: "article-2", EditorialScore: 90, ClassifierVersion: "test-model-v1", FetchedAt: now.Add(-1 * time.Hour)},
	}
	sources := &fakeSourceRepo{}
	signals := &fakeSignalsRepo{unknownEntities: map[string]int{}, pendingDict: map[string]int{}}
	queue := newFakeQueueRepo()
	hardExamples := &fakeHardExampleRepo{}
	queue.upserted["article-purged"] = reviewpriority.QueueEntry{ArticleID: "article-purged"}

	result, err := editorialreview.Sync(ctx, proposals, articles, sources, signals, queue, hardExamples, testModel(), testScoringConfig(), now)
	if err != nil {
		t.Fatalf("Sync() error = %v, want nil (orphaned proposals must not abort)", err)
	}
	if result.Orphaned != 1 {
		t.Errorf("result.Orphaned = %d, want 1", result.Orphaned)
	}
	if !queue.deleted["article-purged"] {
		t.Errorf("article-purged's queue row should have been deleted")
	}
	// The other pending proposal must still be processed normally, not
	// short-circuited by the orphan.
	if result.Queued != 1 {
		t.Errorf("result.Queued = %d, want 1 (article-2 should still be processed)", result.Queued)
	}
	if _, ok := queue.upserted["article-2"]; !ok {
		t.Errorf("article-2 should have been upserted")
	}
}

// TestSync_NeverEvaluatedArticleIsNotSkippedAsObviousAgreement covers
// Finding 2 (final review): an article with EditorialScore == 0 and
// ClassifierVersion == "" (never scored by the NB classifier) must not
// be treated as a confident NB reject, even when combined with a
// confident LLM "discard" -- that combination must not resolve to
// Score.Skip (an "obvious agreement" silently dropped from the queue).
func TestSync_NeverEvaluatedArticleIsNotSkippedAsObviousAgreement(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	proposals := &fakeProposalRepo{pending: []editoriallabelproposal.Proposal{
		{ID: "proposal-1", ArticleID: "article-1", Label: "discard", Confidence: 0.99},
	}}
	articles := fakeArticleGetter{
		"article-1": article.Article{ID: "article-1", EditorialScore: 0, ClassifierVersion: "", FetchedAt: now.Add(-1 * time.Hour)},
	}
	sources := &fakeSourceRepo{}
	signals := &fakeSignalsRepo{unknownEntities: map[string]int{}, pendingDict: map[string]int{}}
	queue := newFakeQueueRepo()
	hardExamples := &fakeHardExampleRepo{}

	result, err := editorialreview.Sync(ctx, proposals, articles, sources, signals, queue, hardExamples, testModel(), testScoringConfig(), now)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Skipped != 0 || result.Queued != 1 {
		t.Errorf("result = %+v, want Queued=1, Skipped=0 (never-evaluated article must not be an obvious agreement)", result)
	}
	entry, ok := queue.upserted["article-1"]
	if !ok {
		t.Fatalf("article-1 should have been queued, not skipped")
	}
	if entry.NBZone != editorial.ZoneAmbiguous {
		t.Errorf("entry.NBZone = %s, want ambiguous (never evaluated)", entry.NBZone)
	}
}

// TestSync_HardMiningSignalIsPopulated covers the whole-branch review's
// Finding 1: reviewpriority.Signals.HardMiningCount existed and was
// scored, but nothing in production ever set it. Two proposals that are
// identical in every other respect must differ in final score by exactly
// the hard_mining component's weight when one of the two articles has an
// unresolved hard_example_candidates row.
func TestSync_HardMiningSignalIsPopulated(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	proposals := &fakeProposalRepo{pending: []editoriallabelproposal.Proposal{
		{ID: "proposal-hard", ArticleID: "article-hard", Label: "discard", Confidence: 0.7},
		{ID: "proposal-plain", ArticleID: "article-plain", Label: "discard", Confidence: 0.7},
	}}
	articles := fakeArticleGetter{
		"article-hard":  {ID: "article-hard", EditorialScore: 90, ClassifierVersion: "test-model-v1", FetchedAt: now.Add(-1 * time.Hour)},
		"article-plain": {ID: "article-plain", EditorialScore: 90, ClassifierVersion: "test-model-v1", FetchedAt: now.Add(-1 * time.Hour)},
	}
	sources := &fakeSourceRepo{}
	signals := &fakeSignalsRepo{unknownEntities: map[string]int{}, pendingDict: map[string]int{}}
	queue := newFakeQueueRepo()
	hardExamples := &fakeHardExampleRepo{unresolved: map[string]bool{"article-hard": true}}

	cfg := testScoringConfig()
	if _, err := editorialreview.Sync(ctx, proposals, articles, sources, signals, queue, hardExamples, testModel(), cfg, now); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	hard, ok := queue.upserted["article-hard"]
	if !ok {
		t.Fatalf("article-hard was not upserted into the queue")
	}
	plain, ok := queue.upserted["article-plain"]
	if !ok {
		t.Fatalf("article-plain was not upserted into the queue")
	}

	delta := hard.Score - plain.Score
	if diff := delta - cfg.Weights.HardMining; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("score delta = %f, want exactly the hard_mining weight %f (article-hard has an unresolved candidate row, article-plain does not)",
			delta, cfg.Weights.HardMining)
	}
}

// TestSync_NilHardExampleRepositoryIsSafe pins the nil-safe contract
// that matches trainrun.Deps.HardExamples: a caller that has not wired
// mining up must still Sync, with HardMiningCount left at 0.
func TestSync_NilHardExampleRepositoryIsSafe(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	proposals := &fakeProposalRepo{pending: []editoriallabelproposal.Proposal{
		{ID: "proposal-1", ArticleID: "article-1", Label: "discard", Confidence: 0.7},
	}}
	articles := fakeArticleGetter{
		"article-1": {ID: "article-1", EditorialScore: 90, ClassifierVersion: "test-model-v1", FetchedAt: now.Add(-1 * time.Hour)},
	}
	sources := &fakeSourceRepo{}
	signals := &fakeSignalsRepo{unknownEntities: map[string]int{}, pendingDict: map[string]int{}}
	queue := newFakeQueueRepo()

	result, err := editorialreview.Sync(ctx, proposals, articles, sources, signals, queue, nil, testModel(), testScoringConfig(), now)
	if err != nil {
		t.Fatalf("Sync() error = %v, want nil (a nil hard-example repository must be safe)", err)
	}
	if result.Queued != 1 {
		t.Errorf("result.Queued = %d, want 1", result.Queued)
	}
}
