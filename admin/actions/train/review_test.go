package train_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/train"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// runAs runs an action with an explicit actor.
func (f fixture) runAs(actor, name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range train.Actions() {
		if c.Name == name {
			a = c
		}
	}
	if a.Name == "" {
		f.t.Fatalf("no action %q", name)
	}
	p, err := action.Decode(a, json.RawMessage(params))
	if err != nil {
		return action.Result{}, err
	}
	env := action.Env{DB: f.db, Config: f.cfg, Mode: action.ModeLocal, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: actor}
	return a.Run(context.Background(), env, p)
}

// seedReview stores an article (body given), a pending proposal and a queue
// entry, returning the proposal id.
func (f fixture) seedReview(id, body string, score float64) string {
	f.t.Helper()
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(f.t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	if _, err := f.db.Exec(`INSERT INTO event_clusters (id, first_seen_at, last_updated_at) VALUES (?, ?, ?)`, "c-"+id, now, now); err != nil {
		f.t.Fatalf("seed cluster: %v", err)
	}
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
		ID: id, Title: "Title " + id, Summary: "Summary " + id, Body: body, Domain: "example.com", ClusterID: "c-" + id,
		OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusFetched,
	}); err != nil {
		f.t.Fatalf("seed article: %v", err)
	}
	pid, err := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(f.db).Save(ctx, editoriallabelproposal.Proposal{
		ArticleID: id, Label: "publish", Confidence: 0.8, Reason: "r", Model: "m",
		PromptVersion: "v1", PolicyVersion: "v1", Status: editoriallabelproposal.StatusPending, CreatedAt: now,
	})
	if err != nil {
		f.t.Fatalf("seed proposal: %v", err)
	}
	if err := sqlitereviewqueue.NewReviewQueueRepository(f.db).Upsert(ctx, reviewpriority.QueueEntry{
		ArticleID: id, ProposalID: pid, Score: score, Reason: "uncertain", NBZone: editorial.ZoneAmbiguous,
		NBValue: 0.4, LLMLabel: "publish", LLMConfidence: 0.8, SuggestedLabel: "publish",
	}); err != nil {
		f.t.Fatalf("seed queue: %v", err)
	}
	return pid
}

func (f fixture) queueLen() int {
	f.t.Helper()
	es, err := sqlitereviewqueue.NewReviewQueueRepository(f.db).ListOrderedByScore(context.Background(), 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return len(es)
}

func TestReviewActionsMetadata(t *testing.T) {
	for name, kind := range map[string]action.Kind{"train.review_queue": action.KindRead, "train.review_decide": action.KindWrite} {
		var a action.Action
		for _, c := range train.Actions() {
			if c.Name == name {
				a = c
			}
		}
		if a.Name == "" {
			t.Fatalf("missing %s", name)
		}
		if a.Group != "train" || a.Kind != kind || len(a.Modes) != 1 || a.Modes[0] != action.ModeLocal || a.Title == "" || a.Description == "" {
			t.Errorf("%s metadata = %+v", name, a)
		}
		props, _ := action.Schema(a.Params)["properties"].(map[string]any)
		if len(props) == 0 {
			t.Errorf("%s has no params", name)
		}
		for field, v := range props {
			if d, _ := v.(map[string]any)["description"].(string); d == "" {
				t.Errorf("%s param %s has no help", name, field)
			}
		}
	}
}

func TestReviewQueueItems(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	f.seedReview("a2", "", 0.1)
	if err := sqliteadapter.NewHumanLabelRepository(f.db).Label(context.Background(), training.Example{
		ArticleID: "a1", Label: "discard", LabeledAt: now, LabeledBy: "old", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("train.review_queue", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "object" || res.Summary != "2 item(s) in the review queue" {
		t.Fatalf("res = %+v", res)
	}
	raw, _ := json.Marshal(res.Object)
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d", len(got.Items))
	}
	it := got.Items[0]
	for k, want := range map[string]any{
		"article_id": "a1", "proposal_id": pid, "score": 0.9, "reason": "uncertain", "nb_value": 0.4,
		"llm_label": "publish", "llm_confidence": 0.8, "suggested_label": "publish", "purged": false,
		"title": "Title a1", "summary": "Summary a1", "url": "https://example.com/a1", "source_name": "Feed A",
		"source_type": "rss", "domain": "example.com", "published_at": "2026-11-05T12:00:00Z", "cluster_id": "c-a1",
	} {
		if it[k] != want {
			t.Errorf("%s = %v (%T), want %v", k, it[k], it[k], want)
		}
	}
	if _, ok := it["nb_zone"]; !ok {
		t.Error("missing nb_zone")
	}
	prior, _ := it["prior_labels"].([]any)
	if len(prior) != 1 {
		t.Fatalf("prior_labels = %v", it["prior_labels"])
	}
	p0, _ := prior[0].(map[string]any)
	if p0["label"] != "discard" || p0["labeled_by"] != "old" || p0["source"] != "manual" || p0["labeled_at"] != "2026-11-05T12:00:00Z" {
		t.Errorf("prior_labels[0] = %v", p0)
	}
	if pl, ok := got.Items[1]["prior_labels"].([]any); !ok || len(pl) != 0 {
		t.Errorf("prior_labels for a2 = %v, want empty array", got.Items[1]["prior_labels"])
	}
}

// The articles table does not persist Body today, so truncation is checked on
// the helper the item builder uses.
func TestReviewBodyTruncation(t *testing.T) {
	long := strings.Repeat("é", 4500)
	if got, want := train.TruncateRunes(long, 4000), strings.Repeat("é", 4000)+"…"; got != want {
		t.Errorf("long body: got %d runes, want 4000 + ellipsis", len([]rune(got)))
	}
	if got := train.TruncateRunes("short", 4000); got != "short" {
		t.Errorf("short body = %q", got)
	}
	exact := strings.Repeat("x", 4000)
	if got := train.TruncateRunes(exact, 4000); got != exact {
		t.Error("a body of exactly 4000 characters must not be truncated")
	}
}

func TestReviewQueueLimit(t *testing.T) {
	f := newFixture(t)
	f.seedReview("a1", "", 0.9)
	f.seedReview("a2", "", 0.1)
	res, err := f.run("train.review_queue", `{"limit":1}`)
	if err != nil || res.Summary != "1 item(s) in the review queue" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	for _, p := range []string{`{"limit":0}`, `{"limit":501}`} {
		_, err := f.run("train.review_queue", p)
		wantCode(t, err, action.CodeInvalidParams)
	}
}

func TestReviewDecidePublishWritesLabel(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	res, err := f.run("train.review_decide", `{"article_id":"a1","proposal_id":"`+pid+`","decision":"publish"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "object" || res.Summary != "published a1" {
		t.Errorf("res = %+v", res)
	}
	obj, _ := res.Object.(map[string]any)
	if obj["outcome"] != "decided" || obj["article_id"] != "a1" {
		t.Errorf("object = %v", obj)
	}
	got, ok, err := sqliteadapter.NewHumanLabelRepository(f.db).GetActive(context.Background(), "a1")
	if err != nil || !ok {
		t.Fatalf("GetActive: %v %v", ok, err)
	}
	if got.Label != "publish" || got.LabeledBy != "ana" || got.Source != "admin:train-review" {
		t.Errorf("label = %+v", got)
	}
	if f.queueLen() != 0 {
		t.Error("queue entry not deleted")
	}
}

func TestReviewDecideDiscard(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	res, err := f.run("train.review_decide", `{"article_id":"a1","proposal_id":"`+pid+`","decision":"discard"}`)
	if err != nil || res.Summary != "discarded a1" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	got, _, _ := sqliteadapter.NewHumanLabelRepository(f.db).GetActive(context.Background(), "a1")
	if got.Label != "discard" {
		t.Errorf("label = %+v", got)
	}
}

// The admin layer audits write actions automatically; this action-level
// harness does not expose the audit log, so only the stored label is asserted.
func TestReviewDecideMCPLabeledByMCP(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	if _, err := f.runAs("mcp", "train.review_decide", `{"article_id":"a1","proposal_id":"`+pid+`","decision":"publish"}`); err != nil {
		t.Fatal(err)
	}
	got, ok, err := sqliteadapter.NewHumanLabelRepository(f.db).GetActive(context.Background(), "a1")
	if err != nil || !ok {
		t.Fatalf("GetActive: %v %v", ok, err)
	}
	if got.LabeledBy != "mcp" || got.Source != "admin:train-review" {
		t.Errorf("label = %+v", got)
	}
}

func TestReviewDecideAlreadyLabeledIsOutcome(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	if err := sqliteadapter.NewHumanLabelRepository(f.db).Label(context.Background(), training.Example{
		ArticleID: "a1", Label: "discard", LabeledAt: now, LabeledBy: "other", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("train.review_decide", `{"article_id":"a1","proposal_id":"`+pid+`","decision":"publish"}`)
	if err != nil {
		t.Fatal(err)
	}
	if obj, _ := res.Object.(map[string]any); obj["outcome"] != "dropped_already_labeled" ||
		res.Summary != "a1 was already labeled elsewhere; dropped from the queue" {
		t.Errorf("res = %+v", res)
	}
	if got, _, _ := sqliteadapter.NewHumanLabelRepository(f.db).GetActive(context.Background(), "a1"); got.LabeledBy != "other" {
		t.Errorf("label overwritten: %+v", got)
	}
	if f.queueLen() != 0 {
		t.Error("queue entry not deleted")
	}
}

func TestReviewDecidePurgedIsOutcome(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	if _, err := f.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM articles WHERE id = ?`, "a1"); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("train.review_decide", `{"article_id":"a1","proposal_id":"`+pid+`","decision":"publish"}`)
	if err != nil {
		t.Fatal(err)
	}
	if obj, _ := res.Object.(map[string]any); obj["outcome"] != "dropped_purged" || res.Summary != "a1 no longer exists; dropped from the queue" {
		t.Errorf("res = %+v", res)
	}
}

func TestReviewDecideUnknownEntryIsNotFound(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	_, err := f.run("train.review_decide", `{"article_id":"nope","proposal_id":"`+pid+`","decision":"publish"}`)
	wantCode(t, err, action.CodeNotFound)
	_, err = f.run("train.review_decide", `{"article_id":"a1","proposal_id":"other","decision":"publish"}`)
	wantCode(t, err, action.CodeNotFound)
	if f.queueLen() != 1 {
		t.Error("queue must be untouched")
	}
}

func TestReviewDecideRejectsOtherDecision(t *testing.T) {
	f := newFixture(t)
	pid := f.seedReview("a1", "", 0.9)
	_, err := f.run("train.review_decide", `{"article_id":"a1","proposal_id":"`+pid+`","decision":"skip"}`)
	wantCode(t, err, action.CodeInvalidParams)
	_, err = f.run("train.review_decide", `{"article_id":"a1","decision":"publish"}`)
	wantCode(t, err, action.CodeInvalidParams)
}
