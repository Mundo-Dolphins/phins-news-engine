package train

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

type fakeArticles map[string]article.Article

// FindByID returns a wrapped article.ErrNotFound for any id not present
// in the map, simulating an article deleted by a retention purge after
// its queue entry was created.
func (f fakeArticles) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f[id]
	if !ok {
		return article.Article{}, fmt.Errorf("fakeArticles: %s: %w", id, article.ErrNotFound)
	}
	return a, nil
}

type fakeLabels struct {
	labeled []training.Example
	// errByArticle, when set for an ArticleID, is returned by Label
	// instead of recording the example -- used to simulate
	// training.ErrAlreadyLabeled and other Label failures.
	errByArticle map[string]error
}

func (f *fakeLabels) Label(_ context.Context, e training.Example) error {
	if err := f.errByArticle[e.ArticleID]; err != nil {
		return err
	}
	f.labeled = append(f.labeled, e)
	return nil
}
func (f *fakeLabels) Relabel(context.Context, training.Example, int) error { return nil }
func (f *fakeLabels) Retract(context.Context, string, int) error           { return nil }
func (f *fakeLabels) GetActive(context.Context, string) (training.Example, bool, error) {
	return training.Example{}, false, nil
}

func (f *fakeLabels) ListByArticle(context.Context, string) ([]training.Example, error) {
	return nil, nil
}

func (f *fakeLabels) ListAll(context.Context) ([]training.Example, error) { return nil, nil }

func (f *fakeLabels) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return nil, nil
}
func (f *fakeLabels) CountEditorialExamples(context.Context) (int, error) { return 0, nil }
func (f *fakeLabels) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}

func (f *fakeLabels) CountUnresolvedConflicts(context.Context) (int, error)           { return 0, nil }
func (f *fakeLabels) ResolveConflict(context.Context, string, training.Example) error { return nil }

type fakeProposals struct {
	updated map[string]editoriallabelproposal.Status
}

func (f *fakeProposals) Save(context.Context, editoriallabelproposal.Proposal) (string, error) {
	return "", nil
}

func (f *fakeProposals) GetLatestByArticle(context.Context, string) (editoriallabelproposal.Proposal, bool, error) {
	return editoriallabelproposal.Proposal{}, false, nil
}

func (f *fakeProposals) ListLatestPerArticle(context.Context) ([]editoriallabelproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeProposals) ListByStatus(context.Context, editoriallabelproposal.Status) ([]editoriallabelproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeProposals) UpdateStatus(_ context.Context, id string, status editoriallabelproposal.Status, _ time.Time) error {
	f.updated[id] = status
	return nil
}

func (f *fakeProposals) FilterUnprocessed(context.Context, string, []string) ([]string, error) {
	return nil, nil
}

func (f *fakeProposals) MarkArticlesProcessed(context.Context, string, []string, time.Time) error {
	return nil
}

type fakeQueue struct {
	deleted []string
}

func (f *fakeQueue) Upsert(context.Context, reviewpriority.QueueEntry) error { return nil }
func (f *fakeQueue) Delete(_ context.Context, articleID string) error {
	f.deleted = append(f.deleted, articleID)
	return nil
}

func (f *fakeQueue) ListOrderedByScore(context.Context, int) ([]reviewpriority.QueueEntry, error) {
	return nil, nil
}

func testEntries() []reviewpriority.QueueEntry {
	return []reviewpriority.QueueEntry{
		{ArticleID: "article-1", ProposalID: "proposal-1", Score: 0.9, Reason: "disagreement", NBZone: editorial.ZoneAccept, LLMLabel: "discard", SuggestedLabel: "discard"},
		{ArticleID: "article-2", ProposalID: "proposal-2", Score: 0.8, Reason: "uncertain", NBZone: editorial.ZoneAmbiguous, LLMLabel: "publish", SuggestedLabel: "publish"},
		{ArticleID: "article-3", ProposalID: "proposal-3", Score: 0.7, Reason: "novel vocab", NBZone: editorial.ZoneAmbiguous, LLMLabel: "uncertain", SuggestedLabel: "uncertain"},
	}
}

func TestRunReviewLoop_PublishDiscardSkip(t *testing.T) {
	articles := fakeArticles{
		"article-1": {ID: "article-1", Title: "T1", Summary: "S1"},
		"article-2": {ID: "article-2", Title: "T2", Summary: "S2"},
		"article-3": {ID: "article-3", Title: "T3", Summary: "S3"},
	}
	labels := &fakeLabels{}
	proposals := &fakeProposals{updated: map[string]editoriallabelproposal.Status{}}
	queue := &fakeQueue{}
	var out bytes.Buffer

	published, discarded, skipped, err := runReviewLoop(
		context.Background(), &out, bufio.NewReader(strings.NewReader("p\nd\ns\n")),
		testEntries(), articles, labels, proposals, queue, "reviewer-1", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("runReviewLoop() error = %v", err)
	}
	if published != 1 || discarded != 1 || skipped != 1 {
		t.Fatalf("published=%d discarded=%d skipped=%d, want 1,1,1", published, discarded, skipped)
	}

	if len(labels.labeled) != 2 {
		t.Fatalf("len(labels.labeled) = %d, want 2", len(labels.labeled))
	}
	if labels.labeled[0].ArticleID != "article-1" || labels.labeled[0].Label != "publish" || labels.labeled[0].Source != "cli:train-review" || labels.labeled[0].LabeledBy != "reviewer-1" {
		t.Errorf("labels.labeled[0] = %+v, unexpected fields", labels.labeled[0])
	}
	if labels.labeled[1].ArticleID != "article-2" || labels.labeled[1].Label != "discard" {
		t.Errorf("labels.labeled[1] = %+v, unexpected fields", labels.labeled[1])
	}

	if proposals.updated["proposal-1"] != editoriallabelproposal.StatusApproved {
		t.Errorf("proposal-1 status = %v, want approved", proposals.updated["proposal-1"])
	}
	if proposals.updated["proposal-2"] != editoriallabelproposal.StatusRejected {
		t.Errorf("proposal-2 status = %v, want rejected", proposals.updated["proposal-2"])
	}
	if _, ok := proposals.updated["proposal-3"]; ok {
		t.Errorf("proposal-3 should not have been updated (skipped)")
	}

	if len(queue.deleted) != 2 || queue.deleted[0] != "article-1" || queue.deleted[1] != "article-2" {
		t.Errorf("queue.deleted = %v, want [article-1, article-2] (skip must not delete)", queue.deleted)
	}

	printed := out.String()
	for _, want := range []string{"T1", "S1", "discard", "disagreement", "T2", "T3"} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed output missing %q; got:\n%s", want, printed)
		}
	}
}

func TestRunReviewLoop_QuitStopsImmediately(t *testing.T) {
	articles := fakeArticles{"article-1": {ID: "article-1"}, "article-2": {ID: "article-2"}, "article-3": {ID: "article-3"}}
	labels := &fakeLabels{}
	proposals := &fakeProposals{updated: map[string]editoriallabelproposal.Status{}}
	queue := &fakeQueue{}
	var out bytes.Buffer

	published, discarded, skipped, err := runReviewLoop(
		context.Background(), &out, bufio.NewReader(strings.NewReader("q\n")),
		testEntries(), articles, labels, proposals, queue, "reviewer-1", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("runReviewLoop() error = %v", err)
	}
	if published != 0 || discarded != 0 || skipped != 0 {
		t.Fatalf("published=%d discarded=%d skipped=%d, want 0,0,0", published, discarded, skipped)
	}
	if len(labels.labeled) != 0 || len(queue.deleted) != 0 {
		t.Errorf("quit must write nothing: labels=%v queue.deleted=%v", labels.labeled, queue.deleted)
	}
}

func TestRunReviewLoop_ExhaustedInputStopsGracefully(t *testing.T) {
	articles := fakeArticles{"article-1": {ID: "article-1"}, "article-2": {ID: "article-2"}, "article-3": {ID: "article-3"}}
	labels := &fakeLabels{}
	proposals := &fakeProposals{updated: map[string]editoriallabelproposal.Status{}}
	queue := &fakeQueue{}
	var out bytes.Buffer

	published, discarded, skipped, err := runReviewLoop(
		context.Background(), &out, bufio.NewReader(strings.NewReader("p\n")), // only one line for 3 entries
		testEntries(), articles, labels, proposals, queue, "reviewer-1", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("runReviewLoop() error = %v", err)
	}
	if published != 1 || discarded != 0 || skipped != 0 {
		t.Fatalf("published=%d discarded=%d skipped=%d, want 1,0,0", published, discarded, skipped)
	}
}

func TestRunReviewLoop_UnrecognizedInputIsTreatedAsSkip(t *testing.T) {
	articles := fakeArticles{"article-1": {ID: "article-1"}, "article-2": {ID: "article-2"}, "article-3": {ID: "article-3"}}
	labels := &fakeLabels{}
	proposals := &fakeProposals{updated: map[string]editoriallabelproposal.Status{}}
	queue := &fakeQueue{}
	var out bytes.Buffer

	published, discarded, skipped, err := runReviewLoop(
		context.Background(), &out, bufio.NewReader(strings.NewReader("x\nq\n")),
		testEntries()[:1], articles, labels, proposals, queue, "reviewer-1", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("runReviewLoop() error = %v", err)
	}
	if skipped != 1 || published != 0 || discarded != 0 {
		t.Fatalf("published=%d discarded=%d skipped=%d, want 0,0,1", published, discarded, skipped)
	}
}

// TestRunReviewLoop_OrphanedArticleDoesNotAbort covers Finding 1 (final
// review): a queue entry whose article was deleted by a retention purge
// must be dropped from the queue, not treated as a fatal error that
// kills the whole interactive session, and subsequent entries must
// still be processed.
func TestRunReviewLoop_OrphanedArticleDoesNotAbort(t *testing.T) {
	// article-1 is intentionally absent: fakeArticles.FindByID returns a
	// wrapped article.ErrNotFound for it, simulating the purge.
	articles := fakeArticles{"article-2": {ID: "article-2", Title: "T2", Summary: "S2"}}
	labels := &fakeLabels{}
	proposals := &fakeProposals{updated: map[string]editoriallabelproposal.Status{}}
	queue := &fakeQueue{}
	var out bytes.Buffer

	published, discarded, skipped, err := runReviewLoop(
		context.Background(), &out, bufio.NewReader(strings.NewReader("p\n")),
		testEntries()[:2], articles, labels, proposals, queue, "reviewer-1", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("runReviewLoop() error = %v, want nil (orphaned entry must not abort)", err)
	}
	if published != 1 || discarded != 0 || skipped != 0 {
		t.Fatalf("published=%d discarded=%d skipped=%d, want 1,0,0 (article-2 still processed)", published, discarded, skipped)
	}
	if len(queue.deleted) != 2 || queue.deleted[0] != "article-1" || queue.deleted[1] != "article-2" {
		t.Errorf("queue.deleted = %v, want [article-1, article-2] (orphan dropped, article-2 published)", queue.deleted)
	}
}

// TestRunReviewLoop_AlreadyLabeledElsewhereDropsEntry covers Finding 4
// (final review): when Label returns training.ErrAlreadyLabeled (the
// article was labeled through some other path after being queued),
// runReviewLoop must not abort the whole session -- it drops that entry
// from the queue and keeps processing the rest.
func TestRunReviewLoop_AlreadyLabeledElsewhereDropsEntry(t *testing.T) {
	articles := fakeArticles{
		"article-1": {ID: "article-1", Title: "T1", Summary: "S1"},
		"article-2": {ID: "article-2", Title: "T2", Summary: "S2"},
	}
	labels := &fakeLabels{errByArticle: map[string]error{
		"article-1": fmt.Errorf("sqlite: label article-1: %w", training.ErrAlreadyLabeled),
	}}
	proposals := &fakeProposals{updated: map[string]editoriallabelproposal.Status{}}
	queue := &fakeQueue{}
	var out bytes.Buffer

	published, discarded, skipped, err := runReviewLoop(
		context.Background(), &out, bufio.NewReader(strings.NewReader("p\nd\n")),
		testEntries()[:2], articles, labels, proposals, queue, "reviewer-1", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("runReviewLoop() error = %v, want nil (already-labeled entry must not abort)", err)
	}
	if published != 0 {
		t.Errorf("published = %d, want 0 (article-1's Label call failed with ErrAlreadyLabeled)", published)
	}
	if discarded != 1 {
		t.Errorf("discarded = %d, want 1 (article-2 still processed)", discarded)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (article-1 dropped as already-labeled)", skipped)
	}
	if len(queue.deleted) != 2 || queue.deleted[0] != "article-1" || queue.deleted[1] != "article-2" {
		t.Errorf("queue.deleted = %v, want [article-1, article-2]", queue.deleted)
	}
	if _, ok := proposals.updated["proposal-1"]; ok {
		t.Errorf("proposal-1 should not have been updated (already labeled elsewhere, dropped instead)")
	}
	if proposals.updated["proposal-2"] != editoriallabelproposal.StatusRejected {
		t.Errorf("proposal-2 status = %v, want rejected", proposals.updated["proposal-2"])
	}
	if !strings.Contains(out.String(), "article-1") {
		t.Errorf("expected output to mention article-1's drop; got:\n%s", out.String())
	}
}

func TestNewReviewCommand_HasExpectedFlags(t *testing.T) {
	cmd := newReviewCommand()
	if cmd.Flags().Lookup("by") == nil {
		t.Errorf("missing --by flag")
	}
	if cmd.Flags().Lookup("limit") == nil {
		t.Errorf("missing --limit flag")
	}
	if l, err := cmd.Flags().GetInt("limit"); err != nil || l != 50 {
		t.Errorf("limit default = %d, err %v, want 50", l, err)
	}
}
