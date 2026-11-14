package cycle_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/cycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// longSummary is longer than any truncation the CLI or a card could apply.
var longSummary = strings.Repeat("The isotopes rally late in a long and detailed match report. ", 12)

// seedReview stores three review candidates (k1 in cluster c1 with a long
// summary, k2 and k3 alone) and seven already-labelled cluster mates of k1,
// which selection never offers. It returns the mates' ids.
func seedReview(t *testing.T, db *sql.DB) []string {
	t.Helper()
	ctx := context.Background()
	articles := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"}
	if _, err := db.Exec(`INSERT INTO event_clusters (id, first_seen_at, last_updated_at) VALUES ('c1', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	save := func(id, title, summary, cluster, role string, at time.Time) {
		t.Helper()
		if err := articles.Save(ctx, article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Title: title, Summary: summary, FetchedAt: at, PublishedAt: at, Status: article.StatusPendingPublication,
			Source: src, ClusterID: cluster, ClusterMemberRole: role,
		}); err != nil {
			t.Fatal(err)
		}
	}
	save("k1", "Isotopes sign a new kicker", longSummary, "c1", "primary", now.Add(-48*time.Hour))
	save("k2", "Isotopes practice report", "short", "", "", now.Add(-47*time.Hour))
	save("k3", "Isotopes injury update", "short", "", "", now.Add(-46*time.Hour))
	var mates []string
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("m%d", i)
		save(id, fmt.Sprintf("Kicker signing story %d", i), "mate", "c1", "member", now.Add(-time.Duration(40-i)*time.Hour))
		if err := labels.Label(ctx, training.Example{ArticleID: id, Label: "publish", LabeledAt: now.Add(-24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		mates = append(mates, id)
	}
	return mates
}

type reviewSource struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type reviewItem struct {
	ArticleID   string       `json:"article_id"`
	Index       int          `json:"index"`
	Title       string       `json:"title"`
	Summary     string       `json:"summary"`
	URL         string       `json:"url"`
	Source      reviewSource `json:"source"`
	PublishedAt time.Time    `json:"published_at"`
	Score       float64      `json:"score"`
	Ambiguity   float64      `json:"ambiguity"`
	PriorLabel  *struct {
		Label string `json:"label"`
		By    string `json:"by"`
	} `json:"prior_label"`
	Cluster struct {
		ID     string `json:"id"`
		Role   string `json:"role"`
		Others []struct {
			ArticleID   string    `json:"article_id"`
			Title       string    `json:"title"`
			Source      string    `json:"source"`
			URL         string    `json:"url"`
			PublishedAt time.Time `json:"published_at"`
		} `json:"others"`
	} `json:"cluster"`
	Decision *struct {
		Action string `json:"action"`
		Notes  string `json:"notes"`
	} `json:"decision"`
}

type reviewQueue struct {
	Items     []reviewItem `json:"items"`
	Total     int          `json:"total"`
	Undecided int          `json:"undecided"`
}

type reviewOpen struct {
	SessionID string `json:"session_id"`
	Total     int    `json:"total"`
	Decided   int    `json:"decided"`
}

// openReview starts a cycle at the article review step and opens it.
func (f *fixture) openReview() reviewOpen {
	f.t.Helper()
	f.mustRun("cycle.start", `{"sample_size":3}`)
	f.atStep("article_review")
	return decode[reviewOpen](f.t, f.mustRun("cycle.review_open", `{}`))
}

func (f *fixture) queue() reviewQueue {
	f.t.Helper()
	return decode[reviewQueue](f.t, f.mustRun("cycle.review_queue", `{}`))
}

func (f *fixture) decide(articleID, act string) {
	f.t.Helper()
	f.mustRun("cycle.review_decide", fmt.Sprintf(`{"article_id":%q,"action":%q}`, articleID, act))
}

func wantInvalid(t *testing.T, err error, contains string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid params", err)
	}
	if !strings.Contains(ae.Message, contains) {
		t.Fatalf("invalid params = %q, want it to contain %q", ae.Message, contains)
	}
}

func TestReviewQueueCardFields(t *testing.T) {
	f := newFixture(t)
	mates := seedReview(t, f.db)
	opened := f.openReview()
	if opened.SessionID == "" || opened.Total != 3 || opened.Decided != 0 {
		t.Fatalf("open = %+v", opened)
	}
	if got := f.state().Cycle.SessionID; got != opened.SessionID {
		t.Fatalf("wizard session = %q, want %q", got, opened.SessionID)
	}
	q := f.queue()
	if q.Total != 3 || q.Undecided != 3 || len(q.Items) != 3 {
		t.Fatalf("queue = %+v", q)
	}
	frozen, err := sqliteadapter.NewEditorialCycleSessionCandidateRepository(f.db).ListFrozen(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var k1 *reviewItem
	for i := range q.Items {
		it := &q.Items[i]
		if it.ArticleID != frozen[i].ArticleID || it.Index != i+1 {
			t.Errorf("item %d = %s (index %d), want the frozen order %s", i, it.ArticleID, it.Index, frozen[i].ArticleID)
		}
		if it.ArticleID == "k1" {
			k1 = it
		}
	}
	if k1 == nil {
		t.Fatal("k1 not in the queue")
	}
	if k1.Title != "Isotopes sign a new kicker" || k1.Summary != longSummary || k1.URL != "https://example.com/k1" {
		t.Errorf("card = %+v", k1)
	}
	if k1.Source != (reviewSource{Name: "Test Feed", Type: "rss"}) || !k1.PublishedAt.Equal(now.Add(-48*time.Hour)) {
		t.Errorf("source/published = %+v %v", k1.Source, k1.PublishedAt)
	}
	if k1.PriorLabel != nil || k1.Decision != nil {
		t.Errorf("prior label / decision = %+v %+v", k1.PriorLabel, k1.Decision)
	}
	if k1.Cluster.ID != "c1" || k1.Cluster.Role != "primary" || len(k1.Cluster.Others) != len(mates) {
		t.Fatalf("cluster = %+v, want all %d mates", k1.Cluster, len(mates))
	}
	seen := map[string]bool{}
	for _, o := range k1.Cluster.Others {
		seen[o.ArticleID] = true
		if o.Source != "Test Feed" || o.URL != "https://example.com/"+o.ArticleID || o.Title == "" || o.PublishedAt.IsZero() {
			t.Errorf("other = %+v", o)
		}
	}
	for _, m := range mates {
		if !seen[m] {
			t.Errorf("mate %s missing", m)
		}
	}
	for _, it := range q.Items {
		if it.ArticleID != "k1" && (it.Cluster.ID != "" || len(it.Cluster.Others) != 0) {
			t.Errorf("%s cluster = %+v, want none", it.ArticleID, it.Cluster)
		}
	}
}

func TestReviewDecideKeyMapping(t *testing.T) {
	f := newFixture(t)
	seedReview(t, f.db)
	opened := f.openReview()
	ctx := context.Background()
	labels := sqliteadapter.NewHumanLabelRepository(f.db)
	decisions := sqliteadapter.NewReviewDecisionRepository(f.db)
	for _, act := range []reviewdecision.Action{
		reviewdecision.ActionPublish, reviewdecision.ActionReject, reviewdecision.ActionAmbiguous,
		reviewdecision.ActionDuplicate, reviewdecision.ActionSameNews, reviewdecision.ActionUpdate,
		reviewdecision.ActionEditCategory, reviewdecision.ActionSkip,
	} {
		f.decide("k2", string(act))
		outcome, err := reviewdecision.Resolve(act)
		if err != nil {
			t.Fatal(err)
		}
		all, err := decisions.ListBySession(ctx, opened.SessionID)
		if err != nil || len(all) != 1 {
			t.Fatalf("%s: decisions = %+v, %v", act, all, err)
		}
		if d := all[0]; d.ArticleID != "k2" || d.Action != string(act) || d.Label != outcome.Label || d.Reason != outcome.Reason {
			t.Errorf("%s: decision = %+v, want %+v", act, d, outcome)
		}
		active, has, err := labels.GetActive(ctx, "k2")
		if err != nil {
			t.Fatal(err)
		}
		if has != outcome.Persist || (has && (active.Label != outcome.Label || active.Source != "editorial_cycle" || active.SessionID != opened.SessionID)) {
			t.Errorf("%s: active label = %+v (%v), want persist=%v %s", act, active, has, outcome.Persist, outcome.Label)
		}
		for _, it := range f.queue().Items {
			if it.ArticleID == "k2" && (it.Decision == nil || it.Decision.Action != string(act)) {
				t.Errorf("%s: queue decision = %+v", act, it.Decision)
			}
		}
	}
	_, err := f.run("cycle.review_decide", `{"article_id":"k2","action":"bogus"}`)
	wantInvalid(t, err, "action")
	_, err = f.run("cycle.review_decide", `{"article_id":"m0","action":"publish"}`)
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeNotFound {
		t.Fatalf("article outside the session: err = %v, want not found", err)
	}
}

func TestEditCategoryWithdrawsLabel(t *testing.T) {
	f := newFixture(t)
	seedReview(t, f.db)
	f.openReview()
	labels := sqliteadapter.NewHumanLabelRepository(f.db)
	f.decide("k1", "publish")
	if active, has, _ := labels.GetActive(context.Background(), "k1"); !has || active.Label != "publish" {
		t.Fatalf("after publish: %+v %v", active, has)
	}
	f.decide("k1", "edit_category")
	if active, has, err := labels.GetActive(context.Background(), "k1"); err != nil || has {
		t.Fatalf("after edit_category: active = %+v %v %v, want the label withdrawn", active, has, err)
	}
	for _, it := range f.queue().Items {
		if it.ArticleID == "k1" && (it.PriorLabel == nil || it.PriorLabel.Label != "publish") {
			// As the CLI: the prior label is the last one in the history.
			t.Errorf("prior label = %+v", it.PriorLabel)
		}
	}
}

func TestReviewFinishRefusesUndecided(t *testing.T) {
	f := newFixture(t)
	seedReview(t, f.db)
	opened := f.openReview()
	f.decide("k1", "publish")
	f.decide("k2", "reject")
	_, err := f.run("cycle.review_finish", `{}`)
	wantConflict(t, err, "1 article(s) still undecided")
	if _, err := f.run("cycle.next", `{}`); err == nil {
		t.Fatal("next accepted with the review unfinished")
	}
	f.decide("k3", "duplicate")
	f.mustRun("cycle.review_finish", `{}`)
	s := f.state().Cycle.Steps["article_review"]
	if s.Status != "done" {
		t.Fatalf("step = %+v", s)
	}
	var r struct {
		ArticlesReviewed int `json:"articles_reviewed"`
		Publish          int `json:"publish"`
		Reject           int `json:"reject"`
	}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	// duplicate is a reject label, as the CLI counts it.
	if r.ArticlesReviewed != 3 || r.Publish != 1 || r.Reject != 2 {
		t.Errorf("result = %+v", r)
	}
	session, err := sqliteadapter.NewEditorialCycleSessionRepository(f.db).Get(context.Background(), opened.SessionID)
	if err != nil || session.Status != "completed" {
		t.Fatalf("session = %+v, %v", session, err)
	}
	_, err = f.run("cycle.review_decide", `{"article_id":"k1","action":"reject"}`)
	wantConflict(t, err, "already finished")
	f.mustRun("cycle.next", `{}`)
}

func TestReviewPauseAndResumeAfterRestart(t *testing.T) {
	f := newFixture(t)
	seedReview(t, f.db)
	opened := f.openReview()
	f.decide("k2", "ambiguous")
	before := f.queue()
	f.mustRun("cycle.review_pause", `{}`)
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(f.db)
	if s, _ := sessions.Get(context.Background(), opened.SessionID); s.Status != "paused" {
		t.Fatalf("session after pause = %+v", s)
	}
	assertResumes(t, f, opened, before)
}

func TestReviewResumeAfterRestart(t *testing.T) {
	f := newFixture(t)
	seedReview(t, f.db)
	opened := f.openReview()
	f.decide("k3", "publish")
	before := f.queue()
	// The browser closes and the panel restarts without a pause: the session
	// is still running.
	assertResumes(t, f, opened, before)
}

// assertResumes restarts the panel (store reopened, a new article that a
// fresh selection would offer) and checks the review reopens on the same
// frozen set with the decisions made.
func assertResumes(t *testing.T, f *fixture, opened reviewOpen, before reviewQueue) {
	t.Helper()
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	f.openStore()
	if err := sqliteadapter.NewArticleRepository(f.db).Save(context.Background(), article.Article{
		ID: "late", OriginalURL: "https://example.com/late", CanonicalURL: "https://example.com/late", Title: "Late story",
		FetchedAt: now.Add(-time.Hour), PublishedAt: now.Add(-time.Hour), Status: article.StatusPendingPublication,
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	if v := f.state(); v.Cycle == nil || v.Cycle.Step != "article_review" || v.Cycle.SessionID != opened.SessionID {
		t.Fatalf("state after restart = %+v", v.Cycle)
	}
	reopened := decode[reviewOpen](t, f.mustRun("cycle.review_open", `{}`))
	if reopened.SessionID != opened.SessionID || reopened.Total != opened.Total || reopened.Decided != 1 {
		t.Fatalf("reopened = %+v, opened = %+v", reopened, opened)
	}
	after := f.queue()
	if len(after.Items) != len(before.Items) || after.Undecided != before.Undecided {
		t.Fatalf("queue after = %+v, before = %+v", after, before)
	}
	for i := range after.Items {
		a, b := after.Items[i], before.Items[i]
		if a.ArticleID != b.ArticleID || (a.Decision == nil) != (b.Decision == nil) || (a.Decision != nil && a.Decision.Action != b.Decision.Action) {
			t.Errorf("item %d after = %+v, before = %+v", i, a, b)
		}
	}
	session, err := sqliteadapter.NewEditorialCycleSessionRepository(f.db).Get(context.Background(), opened.SessionID)
	if err != nil || session.Status != "running" {
		t.Fatalf("session = %+v, %v", session, err)
	}
}

func TestReviewNeedsOpenSession(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	_, err := f.run("cycle.review_open", `{}`)
	wantConflict(t, err, "the cycle is at step sync")
	f.atStep("article_review")
	for _, name := range []string{"cycle.review_queue", "cycle.review_finish", "cycle.review_pause"} {
		_, err := f.run(name, `{}`)
		wantConflict(t, err, "open the article review first")
	}
}

// seedSessionDecisions creates review session id with a decision per
// article (articles are created as needed).
func seedSessionDecisions(t *testing.T, db *sql.DB, id string, decisions map[string]string) {
	t.Helper()
	ctx := context.Background()
	if err := sqliteadapter.NewEditorialCycleSessionRepository(db).Create(ctx, out.EditorialCycleSession{
		ID: id, StartedAt: now, UpdatedAt: now, Status: "completed", CurrentStep: "review",
	}); err != nil {
		t.Fatal(err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"}
	for articleID, label := range decisions {
		if err := articles.Save(ctx, article.Article{
			ID: articleID, OriginalURL: "https://example.com/" + articleID, CanonicalURL: "https://example.com/" + articleID,
			Title: "Story " + articleID, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished, Source: src,
		}); err != nil {
			t.Fatal(err)
		}
		if err := sqliteadapter.NewReviewDecisionRepository(db).Save(ctx, out.ReviewDecision{
			SessionID: id, ArticleID: articleID, Action: label, Label: label, DecidedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func seedProposal(t *testing.T, db *sql.DB, name string, status entityproposal.Status, decidedAt time.Time, sources ...string) {
	t.Helper()
	p := entityproposal.Proposal{
		CanonicalName: name, NormalizedName: entityproposal.Normalize(name), EntityType: entityproposal.EntityTypePlayer,
		Kind: entityproposal.KindNewEntity, Status: entityproposal.StatusPending, Occurrences: 1, Confidence: 0.9,
		FirstSeen: now, LastSeen: now, CreatedAt: now, ExtractorVersion: "v1",
	}
	for _, s := range sources {
		p.Sources = append(p.Sources, entityproposal.Source{ArticleID: s, ArticleTitle: "Story " + s})
	}
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if status != entityproposal.StatusPending {
		if err := repo.UpdateStatus(context.Background(), id, status, decidedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEntitiesFinishCountsSessionDecisionsSinceStepEntered(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	seedSessionDecisions(t, f.db, "sess-e", map[string]string{"e1": "publish", "e2": "reject", "e3": "ambiguous"})
	started := now.Add(-time.Hour)
	f.setState(func(m map[string]any) {
		m["step"], m["session_id"] = "entities", "sess-e"
		st := stepMap(t, m, "entities")
		st["entered_at"] = started.Format(time.RFC3339Nano)
		st["result"] = map[string]any{"articles": 2, "proposed": 4, "discovery_failed": false, "discovered": true}
	})
	seedProposal(t, f.db, "Quill Farrowby", entityproposal.StatusApproved, now, "e1")
	seedProposal(t, f.db, "Zeno Ashbury", entityproposal.StatusRejected, now, "e2")
	seedProposal(t, f.db, "Orrin Pellwick", entityproposal.StatusRejected, now, "e1", "e2")
	seedProposal(t, f.db, "Old Decision", entityproposal.StatusApproved, started.Add(-time.Hour), "e1")
	seedProposal(t, f.db, "Ambiguous Source", entityproposal.StatusApproved, now, "e3")
	seedProposal(t, f.db, "Still Pending", entityproposal.StatusPending, time.Time{}, "e1")

	// The catalog regenerates (an approval landed): give it a dictionary.
	_, dict := testsupport.WriteFixtureWithDictionary(t)
	f.cfg.Dictionary.Path = dict
	f.cfg.Clustering.EntityCatalog.GeneratedPath = filepath.Join(t.TempDir(), "generated.yml")
	f.mustRun("cycle.entities_finish", `{}`)
	s := f.state().Cycle.Steps["entities"]
	if s.Status != "done" {
		t.Fatalf("step = %+v", s)
	}
	var r struct {
		Articles        int  `json:"articles"`
		Proposed        int  `json:"proposed"`
		Approved        int  `json:"approved"`
		Rejected        int  `json:"rejected"`
		DiscoveryFailed bool `json:"discovery_failed"`
		Regenerated     bool `json:"regenerated"`
	}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if r.Articles != 2 || r.Proposed != 4 || r.Approved != 1 || r.Rejected != 2 || !r.Regenerated {
		t.Errorf("result = %+v (%s)", r, s.Result)
	}
	_, err := f.run("cycle.entities_finish", `{}`)
	wantConflict(t, err, "already finished")
}

func TestEntitiesFinishNeedsDiscovery(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	f.setState(func(m map[string]any) { m["step"], m["session_id"] = "entities", "sess-e" })
	_, err := f.run("cycle.entities_finish", `{}`)
	wantConflict(t, err, "run the entity discovery first")
}

type fakeEntityLLM struct {
	entities []out.EntityDiscoveryEntity
	err      error
}

func (f *fakeEntityLLM) Discover(context.Context, []out.EntityDiscoveryArticle) (out.EntityDiscoveryResult, error) {
	if f.err != nil {
		return out.EntityDiscoveryResult{}, f.err
	}
	return out.EntityDiscoveryResult{Entities: f.entities}, nil
}

func (f *fakeEntityLLM) VerifyCandidate(context.Context, out.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	return weblookup.AnalysisAction{Action: weblookup.ActionFinal}, nil
}

// entityStepFixture is a cycle at the entities step of session sess-1 (one
// published article, a1) with the entity step enabled and llm as the
// discovery LLM; no network is used.
func entityStepFixture(t *testing.T, llm *fakeEntityLLM) *fixture {
	t.Helper()
	f, _ := newTermsFixture(t)
	dir := t.TempDir()
	manual, generated := filepath.Join(dir, "manual.yml"), filepath.Join(dir, "generated.yml")
	const isotopes = "entities:\n    - id: springfield-isotopes\n      canonical_name: Springfield Isotopes\n      kind: team\n      terms:\n        - isotopes\n      is_primary_team: true\n"
	for path, body := range map[string]string{manual: isotopes, generated: "entities: []\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg.Clustering.EntityCatalog.ManualPath, f.cfg.Clustering.EntityCatalog.GeneratedPath = manual, generated
	ed := &f.cfg.EntityDiscovery
	ed.Enabled, ed.ExtractorVersion, ed.BatchSize, ed.Limit = true, "entity-discovery-v1", 5, 50
	ed.LLM = config.EntityDiscoveryLLMConfig{BaseURL: "http://127.0.0.1:1/v1", Model: "test", TimeoutSeconds: 5, MaxTokens: 512}
	ed.Wikidata.Enabled, ed.Roster.Enabled, ed.League.Enabled = false, false, false
	t.Cleanup(cycle.SetDiscoveryLLM(func(config.EntityDiscoveryConfig, int, bool, out.Logger) out.EntityDiscoveryLLM { return llm }))
	f.setState(func(m map[string]any) { m["step"], m["entities_verification"] = "entities", "none" })
	return f
}

func TestEntitiesDiscoverThenFinish(t *testing.T) {
	f := entityStepFixture(t, &fakeEntityLLM{entities: []out.EntityDiscoveryEntity{{
		Name: "Ezo Quarrington", Type: "player", Confidence: 0.9, SourceArticleIDs: []string{"a1"},
		Metadata: map[string]string{"team": "Springfield Isotopes", "team_id": "springfield-isotopes", "position": "CB"},
	}}})
	res := f.mustRun("cycle.entities_discover", `{}`)
	if res.Summary != "entities: 1 articles, 1 batches (0 failed), 1 proposals created or updated" {
		t.Errorf("summary = %q", res.Summary)
	}
	s := f.state().Cycle.Steps["entities"]
	var r struct {
		Articles        int  `json:"articles"`
		Proposed        int  `json:"proposed"`
		Approved        int  `json:"approved"`
		Rejected        int  `json:"rejected"`
		DiscoveryFailed bool `json:"discovery_failed"`
		Discovered      bool `json:"discovered"`
	}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if s.Status != "pending" || r.Articles != 1 || r.Proposed != 1 || r.DiscoveryFailed || !r.Discovered {
		t.Fatalf("after discovery: %+v %s", s, s.Result)
	}
	repo := sqliteentityproposal.NewEntityProposalRepository(f.db)
	pending, err := repo.List(context.Background(), "", string(entityproposal.StatusPending))
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if err := repo.UpdateStatus(context.Background(), pending[0].ID, entityproposal.StatusRejected, now); err != nil {
		t.Fatal(err)
	}
	f.mustRun("cycle.entities_finish", `{}`)
	s = f.state().Cycle.Steps["entities"]
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if s.Status != "done" || r.Rejected != 1 || r.Approved != 0 || r.Proposed != 1 {
		t.Fatalf("after finish: %+v %s", s, s.Result)
	}
}

func TestEntitiesDiscoveryOutageIsAWarning(t *testing.T) {
	f := entityStepFixture(t, &fakeEntityLLM{err: errors.New("connection refused")})
	f.mustRun("cycle.entities_discover", `{}`)
	s := f.state().Cycle.Steps["entities"]
	var r struct {
		DiscoveryFailed bool     `json:"discovery_failed"`
		Warnings        []string `json:"warnings"`
	}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if s.Status != "pending" || !r.DiscoveryFailed || len(r.Warnings) != 1 || !strings.HasPrefix(r.Warnings[0], "WARNING: entity discovery is unavailable") {
		t.Fatalf("step = %+v %s", s, s.Result)
	}
}

func TestEntitiesDiscoverNoReviewedArticles(t *testing.T) {
	f := entityStepFixture(t, &fakeEntityLLM{err: errors.New("must not be called")})
	f.setState(func(m map[string]any) { m["session_id"] = "sess-empty" })
	seedSessionDecisions(t, f.db, "sess-empty", nil)
	res := f.mustRun("cycle.entities_discover", `{}`)
	if res.Summary != "entities: the session has no reviewed articles" {
		t.Errorf("summary = %q", res.Summary)
	}
	if s := f.state().Cycle.Steps["entities"]; s.Status != "done" {
		t.Fatalf("step = %+v", s)
	}
}
