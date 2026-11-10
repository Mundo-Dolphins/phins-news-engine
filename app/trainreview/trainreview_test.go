package trainreview_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainreview"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var testNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type env struct {
	deps      trainreview.Deps
	articles  *sqliteadapter.ArticleRepository
	labels    appout.HumanLabelRepository
	proposals *sqliteeditoriallabelproposal.EditorialLabelProposalRepository
}

func newEnv(t *testing.T) env {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "test.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	labels := sqliteadapter.NewHumanLabelRepository(db)
	proposals := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)
	articles := sqliteadapter.NewArticleRepository(db)
	return env{
		deps: trainreview.Deps{
			Articles:  articles,
			Labels:    labels,
			Proposals: proposals,
			Queue:     sqlitereviewqueue.NewReviewQueueRepository(db),
		},
		articles:  articles,
		labels:    labels,
		proposals: proposals,
	}
}

// seed stores an article, a pending proposal and a queue entry.
func (e env) seed(t *testing.T, id string, score float64) reviewpriority.QueueEntry {
	t.Helper()
	ctx := context.Background()
	sid, err := source.NewID(source.TypeRSS, "feed")
	if err != nil {
		t.Fatalf("source id: %v", err)
	}
	if err := e.articles.Save(ctx, article.Article{
		ID: id, Source: source.Source{ID: sid, Name: "feed", Type: source.TypeRSS}, Title: "Title " + id, Summary: "Summary " + id,
		OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		FetchedAt: testNow, PublishedAt: testNow, Status: article.StatusFetched,
	}); err != nil {
		t.Fatalf("save article: %v", err)
	}
	return e.enqueue(t, id, score)
}

// enqueue stores a proposal and a queue entry without an article row.
func (e env) enqueue(t *testing.T, id string, score float64) reviewpriority.QueueEntry {
	t.Helper()
	ctx := context.Background()
	pid, err := e.proposals.Save(ctx, editoriallabelproposal.Proposal{
		ArticleID: id, Label: "publish", Confidence: 0.8, Reason: "teacher reason " + id, Model: "m",
		PromptVersion: "v1", PolicyVersion: "v1", Status: editoriallabelproposal.StatusPending, CreatedAt: testNow,
	})
	if err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	entry := reviewpriority.QueueEntry{
		ArticleID: id, ProposalID: pid, Score: score, Reason: "uncertain",
		NBZone: editorial.ZoneAmbiguous, LLMLabel: "publish", SuggestedLabel: "publish",
	}
	if err := e.deps.Queue.Upsert(ctx, entry); err != nil {
		t.Fatalf("upsert queue: %v", err)
	}
	return entry
}

func (e env) queueIDs(t *testing.T) []string {
	t.Helper()
	entries, err := e.deps.Queue.ListOrderedByScore(context.Background(), 100)
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	ids := make([]string, 0, len(entries))
	for _, x := range entries {
		ids = append(ids, x.ArticleID)
	}
	return ids
}

func (e env) proposalStatus(t *testing.T, articleID string) editoriallabelproposal.Status {
	t.Helper()
	p, ok, err := e.proposals.GetLatestByArticle(context.Background(), articleID)
	if err != nil || !ok {
		t.Fatalf("get proposal: ok=%v err=%v", ok, err)
	}
	return p.Status
}

func TestDecidePublish(t *testing.T) {
	e := newEnv(t)
	entry := e.seed(t, "a1", 0.9)
	out, err := trainreview.Decide(context.Background(), e.deps, entry, trainreview.Publish, "alice", "admin:train-review", testNow)
	if err != nil || out != trainreview.Decided {
		t.Fatalf("Decide() = %v, %v; want decided, nil", out, err)
	}
	got, ok, err := e.labels.GetActive(context.Background(), "a1")
	if err != nil || !ok {
		t.Fatalf("GetActive: ok=%v err=%v", ok, err)
	}
	if got.Label != "publish" || got.LabeledBy != "alice" || got.Source != "admin:train-review" {
		t.Errorf("label = %+v", got)
	}
	if s := e.proposalStatus(t, "a1"); s != editoriallabelproposal.StatusApproved {
		t.Errorf("proposal status = %v, want approved", s)
	}
	if ids := e.queueIDs(t); len(ids) != 0 {
		t.Errorf("queue = %v, want empty", ids)
	}
}

func TestDecideDiscard(t *testing.T) {
	e := newEnv(t)
	entry := e.seed(t, "a1", 0.9)
	out, err := trainreview.Decide(context.Background(), e.deps, entry, trainreview.Discard, "bob", "cli:train-review", testNow)
	if err != nil || out != trainreview.Decided {
		t.Fatalf("Decide() = %v, %v; want decided, nil", out, err)
	}
	got, _, _ := e.labels.GetActive(context.Background(), "a1")
	if got.Label != "discard" || got.LabeledBy != "bob" || got.Source != "cli:train-review" {
		t.Errorf("label = %+v", got)
	}
	if s := e.proposalStatus(t, "a1"); s != editoriallabelproposal.StatusRejected {
		t.Errorf("proposal status = %v, want rejected", s)
	}
	if ids := e.queueIDs(t); len(ids) != 0 {
		t.Errorf("queue = %v, want empty", ids)
	}
}

func TestDecideAlreadyLabeled(t *testing.T) {
	e := newEnv(t)
	entry := e.seed(t, "a1", 0.9)
	if err := e.labels.Label(context.Background(), training.Example{
		ArticleID: "a1", Label: "discard", LabeledAt: testNow, LabeledBy: "other", Source: "manual",
	}); err != nil {
		t.Fatalf("prior label: %v", err)
	}
	out, err := trainreview.Decide(context.Background(), e.deps, entry, trainreview.Publish, "alice", "admin:train-review", testNow)
	if err != nil || out != trainreview.DroppedAlreadyLabeled {
		t.Fatalf("Decide() = %v, %v; want dropped_already_labeled, nil", out, err)
	}
	if s := e.proposalStatus(t, "a1"); s != editoriallabelproposal.StatusPending {
		t.Errorf("proposal status = %v, want pending (untouched)", s)
	}
	if ids := e.queueIDs(t); len(ids) != 0 {
		t.Errorf("queue = %v, want empty", ids)
	}
	if got, _, _ := e.labels.GetActive(context.Background(), "a1"); got.LabeledBy != "other" {
		t.Errorf("existing label overwritten: %+v", got)
	}
}

func TestDecidePurged(t *testing.T) {
	e := newEnv(t)
	entry := e.enqueue(t, "gone", 0.9)
	out, err := trainreview.Decide(context.Background(), e.deps, entry, trainreview.Publish, "alice", "admin:train-review", testNow)
	if err != nil || out != trainreview.DroppedPurged {
		t.Fatalf("Decide() = %v, %v; want dropped_purged, nil", out, err)
	}
	if ids := e.queueIDs(t); len(ids) != 0 {
		t.Errorf("queue = %v, want empty", ids)
	}
	if _, ok, _ := e.labels.GetActive(context.Background(), "gone"); ok {
		t.Error("purged article must not be labeled")
	}
}

func TestQueueJoinsArticleAndLabels(t *testing.T) {
	e := newEnv(t)
	e.seed(t, "low", 0.2)
	e.seed(t, "high", 0.9)
	if err := e.labels.Label(context.Background(), training.Example{
		ArticleID: "high", Label: "publish", LabeledAt: testNow, LabeledBy: "x", Source: "manual",
	}); err != nil {
		t.Fatalf("label: %v", err)
	}
	items, err := trainreview.Queue(context.Background(), e.deps, e.labels, 10)
	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if len(items) != 2 || items[0].Entry.ArticleID != "high" || items[1].Entry.ArticleID != "low" {
		t.Fatalf("items = %+v, want [high, low]", items)
	}
	if items[0].LLMReason != "teacher reason high" {
		t.Errorf("items[0].LLMReason = %q, want the proposal's reason", items[0].LLMReason)
	}
	if items[0].Article.Title != "Title high" || items[0].Purged {
		t.Errorf("items[0] = %+v", items[0])
	}
	if len(items[0].PriorLabels) != 1 || items[0].PriorLabels[0].Label != "publish" {
		t.Errorf("PriorLabels = %+v", items[0].PriorLabels)
	}
	if len(items[1].PriorLabels) != 0 {
		t.Errorf("low PriorLabels = %+v, want none", items[1].PriorLabels)
	}
}

func TestQueueLLMReasonOnlyFromQueuedProposal(t *testing.T) {
	e := newEnv(t)
	entry := e.seed(t, "a1", 0.5)
	// A newer proposal than the queued one must not lend its reason.
	if err := e.proposals.UpdateStatus(context.Background(), entry.ProposalID, editoriallabelproposal.StatusRejected, testNow); err != nil {
		t.Fatalf("resolve proposal: %v", err)
	}
	if _, err := e.proposals.Save(context.Background(), editoriallabelproposal.Proposal{
		ArticleID: "a1", Label: "discard", Reason: "newer", Model: "m", PromptVersion: "v2", PolicyVersion: "v1",
		Status: editoriallabelproposal.StatusPending, CreatedAt: testNow.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save newer proposal: %v", err)
	}
	items, err := trainreview.Queue(context.Background(), e.deps, e.labels, 10)
	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if len(items) != 1 || items[0].LLMReason != "" {
		t.Fatalf("items = %+v, want one item with no LLM reason", items)
	}
}

func TestQueueMarksPurged(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, "gone", 0.9)
	items, err := trainreview.Queue(context.Background(), e.deps, e.labels, 10)
	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if len(items) != 1 || !items[0].Purged || items[0].Article.ID != "" {
		t.Fatalf("items = %+v, want one purged item with empty article", items)
	}
}

func TestFind(t *testing.T) {
	e := newEnv(t)
	e.seed(t, "a1", 0.5)
	got, ok, err := trainreview.Find(context.Background(), e.deps.Queue, "a1")
	if err != nil || !ok || got.ArticleID != "a1" {
		t.Fatalf("Find(a1) = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := trainreview.Find(context.Background(), e.deps.Queue, "nope"); err != nil || ok {
		t.Fatalf("Find(nope) ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestDecideKeepsEntryRequeuedWithNewProposal(t *testing.T) {
	e := newEnv(t)
	stale := e.seed(t, "a1", 0.9)
	// The teacher re-proposed between Find and Decide: same article, new proposal.
	fresh := stale
	fresh.ProposalID = "newer-proposal"
	if err := e.deps.Queue.Upsert(context.Background(), fresh); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if _, err := trainreview.Decide(context.Background(), e.deps, stale, trainreview.Publish, "alice", "admin:train-review", testNow); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	entries, err := e.deps.Queue.ListOrderedByScore(context.Background(), 10)
	if err != nil || len(entries) != 1 || entries[0].ProposalID != "newer-proposal" {
		t.Fatalf("queue = %+v, err %v; want the re-queued entry to survive", entries, err)
	}
}
