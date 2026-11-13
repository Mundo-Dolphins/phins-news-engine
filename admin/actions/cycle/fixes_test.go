package cycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/cycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// I1: the approval window starts when the wizard enters the entities step,
// so an approval made before the discovery counts and regenerates.
func TestEntitiesFinishCountsApprovalBeforeDiscovery(t *testing.T) {
	f := entityStepFixture(t, &fakeEntityLLM{})
	f.setState(func(m map[string]any) {
		m["step"] = "article_review"
		stepMap(t, m, "article_review")["status"] = "done"
	})
	f.mustRun("cycle.next", `{}`)
	if at := f.state().Cycle.Steps["entities"]; at.Status != "pending" {
		t.Fatalf("entities = %+v", at)
	}
	// Entered an hour ago; approved half an hour ago, before the discovery.
	f.setState(func(m map[string]any) {
		stepMap(t, m, "entities")["entered_at"] = now.Add(-time.Hour).Format(time.RFC3339Nano)
	})
	seedProposal(t, f.db, "Ezo Quarrington", entityproposal.StatusApproved, now.Add(-30*time.Minute), "a1")
	f.mustRun("cycle.entities_discover", `{}`)
	f.mustRun("cycle.entities_finish", `{}`)
	var r struct {
		Approved    int  `json:"approved"`
		Regenerated bool `json:"regenerated"`
	}
	s := f.state().Cycle.Steps["entities"]
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if s.Status != "done" || r.Approved != 1 || !r.Regenerated {
		t.Fatalf("step = %+v %s", s, s.Result)
	}
}

func TestNextRecordsEnteredAt(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	f.mustRun("cycle.next", `{"skip":true}`)
	raw, _, err := f.st.GetState(context.Background(), "editorial_cycle_wizard")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Steps map[string]struct {
			EnteredAt *time.Time `json:"entered_at"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sync", "telegram_import"} {
		if at := w.Steps[s].EnteredAt; at == nil || !at.Equal(now) {
			t.Errorf("%s entered_at = %v", s, at)
		}
	}
	if w.Steps["roster_sync"].EnteredAt != nil {
		t.Error("a step not reached has an entered_at")
	}
}

type fakeRuleLLM struct{ err error }

func (l fakeRuleLLM) DiscoverTopics(context.Context, []out.EventRuleArticle) ([]out.EventTopicProposal, error) {
	return nil, l.err
}

func (l fakeRuleLLM) DiscoverPhrases(context.Context, []out.EventRuleArticle, []string, []string) ([]out.EventPhraseProposal, error) {
	return nil, l.err
}

// eventRulesFixture is a cycle at the event rules step with one generic
// article in the sample and the discovery LLM replaced by llm.
func eventRulesFixture(t *testing.T, llm out.EventRuleDiscoveryLLM) *fixture {
	t.Helper()
	f := entityStepFixture(t, &fakeEntityLLM{})
	f.cfg.Clustering.RulesPath = repofs.ExampleConfig(t, "event_clustering_rules.yml")
	f.cfg.Clustering.GeneratedRulesPath = filepath.Join(t.TempDir(), "generated_rules.yml")
	f.cfg.OfflineTraining.LLM.BaseURL, f.cfg.OfflineTraining.LLM.Model = "http://llm.invalid", "m"
	t.Cleanup(cycle.SetEventRuleLLM(llm))
	f.setState(func(m map[string]any) { m["step"] = "event_rules" })
	ctx := context.Background()
	at := now.Add(-48 * time.Hour)
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
		ID: "gen-1", OriginalURL: "https://news.example.org/gen-1", CanonicalURL: "https://news.example.org/gen-1",
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "wire-1"), Type: source.TypeRSS},
		Title:  "Springfield Isotopes inks a deal with Tavion Dale", FetchedAt: at, PublishedAt: at, Status: article.StatusFetched,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.NewClusterRepository(f.db).SaveRepresentation(ctx, eventsignals.Representation{
		ArticleID: "gen-1", SourceID: "wire-1", Domain: "news.example.org", ContentType: article.ContentTypeArticle,
		PublishedAt: at, FetchedAt: at, EventDate: at, EventWindow: 6 * time.Hour, EventType: eventsignals.EventTypeGeneric,
	}, "rules-v1", at); err != nil {
		t.Fatal(err)
	}
	return f
}

func seedApprovedPhrase(t *testing.T, f *fixture, phrase string) {
	t.Helper()
	repo := sqliteeventruleproposal.NewRepository(f.db)
	p, err := repo.Upsert(context.Background(), eventruleproposal.Proposal{
		Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: "sign",
		Phrase: phrase, EvidenceIDs: []string{"gen-1"}, Status: eventruleproposal.StatusPending,
		PromptVersion: "event-rules-v1", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Status = eventruleproposal.StatusApproved
	if err := repo.Save(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

type ruleCounters struct {
	Sampled          int  `json:"sampled"`
	Proposed         int  `json:"proposed"`
	Approved         int  `json:"approved"`
	Applied          int  `json:"applied"`
	DiscoveryFailed  bool `json:"discovery_failed"`
	GateBlocked      bool `json:"gate_blocked"`
	GateNotExercised bool `json:"gate_not_exercised"`
}

func (f *fixture) ruleStep() (string, ruleCounters) {
	f.t.Helper()
	s := f.state().Cycle.Steps["event_rules"]
	var r ruleCounters
	if len(s.Result) > 0 {
		if err := json.Unmarshal(s.Result, &r); err != nil {
			f.t.Fatal(err)
		}
	}
	return s.Status, r
}

// I2: discovery, apply and finish record the CLI's counters server side.
func TestEventRulesDiscoverApplyFinish(t *testing.T) {
	f := eventRulesFixture(t, fakeRuleLLM{})
	res := f.mustRun("cycle.event_rules_discover", `{}`)
	if !strings.HasPrefix(res.Summary, "1 sampled, 2 calls (0 failed), 0 proposals") {
		t.Errorf("summary = %q", res.Summary)
	}
	if st, r := f.ruleStep(); st != "pending" || r.Sampled != 1 || r.Proposed != 0 || r.DiscoveryFailed {
		t.Fatalf("after discover: %s %+v", st, r)
	}
	seedApprovedPhrase(t, f, "inks a deal")
	t.Cleanup(cycle.SetEventRulesDataset(repofs.Path(t, "testdata", "clustering_groups.yml")))
	res = f.mustRun("cycle.event_rules_apply", `{}`)
	if res.Summary != "PASS: 1 rule(s) applied to "+f.cfg.Clustering.GeneratedRulesPath {
		t.Errorf("summary = %q", res.Summary)
	}
	if got, err := os.ReadFile(f.cfg.Clustering.GeneratedRulesPath); err != nil || !strings.Contains(string(got), "inks a deal") {
		t.Fatalf("generated = %q, %v", got, err)
	}
	st, r := f.ruleStep()
	if st != "pending" || r.Applied != 1 || r.GateBlocked {
		t.Fatalf("after apply: %s %+v", st, r)
	}
	// Nothing left to apply: the counters stay.
	if res := f.mustRun("cycle.event_rules_apply", `{}`); res.Summary != "Nothing to apply." {
		t.Errorf("second apply = %q", res.Summary)
	}
	seedApprovedPhrase(t, f, "agrees to terms")
	f.mustRun("cycle.event_rules_finish", `{}`)
	st, fin := f.ruleStep()
	want := r
	want.Approved = 2 // one applied, one approved not applied
	if st != "done" || fin != want {
		t.Fatalf("after finish: %s %+v, want %+v", st, fin, want)
	}
	_, err := f.run("cycle.event_rules_finish", `{}`)
	wantConflict(t, err, "already finished")
}

func TestEventRulesDiscoveryUnavailable(t *testing.T) {
	f := eventRulesFixture(t, fakeRuleLLM{err: errors.New("connection refused")})
	res := f.mustRun("cycle.event_rules_discover", `{}`)
	if !strings.HasPrefix(res.Summary, "WARNING: 1 sampled") {
		t.Errorf("summary = %q", res.Summary)
	}
	if st, r := f.ruleStep(); st != "pending" || !r.DiscoveryFailed || r.Sampled != 1 {
		t.Fatalf("step = %s %+v", st, r)
	}
}

func TestEventRulesFinishWithoutDiscoveryOrLLM(t *testing.T) {
	f := eventRulesFixture(t, fakeRuleLLM{})
	f.cfg.OfflineTraining.LLM.BaseURL = ""
	_, err := f.run("cycle.event_rules_discover", `{}`)
	wantConflict(t, err, "offline_training.llm")
	f.mustRun("cycle.event_rules_finish", `{}`)
	if st, r := f.ruleStep(); st != "done" || r != (ruleCounters{}) {
		t.Fatalf("step = %s %+v", st, r)
	}
}

func TestEventRulesDisabledStep(t *testing.T) {
	f := eventRulesFixture(t, fakeRuleLLM{})
	f.cfg.Clustering.RulesPath = ""
	for _, name := range []string{"cycle.event_rules_discover", "cycle.event_rules_apply", "cycle.event_rules_finish"} {
		_, err := f.run(name, `{}`)
		wantConflict(t, err, "clustering.rules_path is empty")
	}
}

// M1: a rejected term is in the mining policy file; it cannot be decided
// again. An approved one can.
func TestTermsRejectedCannotBeRedecided(t *testing.T) {
	f, _ := newTermsFixture(t)
	f.mustRun("cycle.terms_prepare", `{}`)
	f.mustRun("cycle.terms_decide", `{"term":"leads","action":"reject"}`)
	_, err := f.run("cycle.terms_decide", `{"term":"leads","action":"approve","category":"coaching_staff"}`)
	wantConflict(t, err, "already rejected and added to ignored_terms")
	f.mustRun("cycle.terms_decide", `{"term":"Ezo Quarrington","action":"approve"}`)
	f.mustRun("cycle.terms_decide", `{"term":"Ezo Quarrington","action":"categorize","category":"coaching_staff"}`)
}

// M3: the cycle's own session is resumed even when another resumable session
// is more recent.
func TestReviewOpenResumesTheCyclesSession(t *testing.T) {
	f := newFixture(t)
	seedReview(t, f.db)
	opened := f.openReview()
	f.decide("k1", "publish")
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(f.db)
	other := out.EditorialCycleSession{
		ID: "other-cli", StartedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour), Status: "paused",
		CurrentStep: "review", FlagsJSON: `{"sample_size":1}`, TotalCandidates: 1,
	}
	if err := sessions.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.NewEditorialCycleSessionCandidateRepository(f.db).SaveAll(context.Background(), "other-cli",
		[]out.EditorialCycleSessionCandidate{{SessionID: "other-cli", ArticleID: "k2", Score: 0.5, SelectedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if latest, err := sessions.GetLatestResumable(context.Background()); err != nil || latest.ID != "other-cli" {
		t.Fatalf("latest resumable = %+v, %v", latest, err)
	}
	reopened := decode[reviewOpen](t, f.mustRun("cycle.review_open", `{}`))
	if reopened.SessionID != opened.SessionID || reopened.Decided != 1 {
		t.Fatalf("reopened = %+v, want session %s", reopened, opened.SessionID)
	}
	if s, _ := sessions.Get(context.Background(), "other-cli"); s.Status != "paused" {
		t.Errorf("other session = %+v, want untouched", s)
	}
}

// M5: the source-policy exclusions are logged with the CLI's line.
func TestReviewOpenLogsSourcePolicyExclusions(t *testing.T) {
	f := newFixture(t)
	seedReview(t, f.db)
	// Freeze the candidates first, then exclude their source.
	opened := f.openReview()
	f.mustRun("cycle.review_pause", `{}`)
	f.cfg.Editorial.ModelBypassSourceIDs = []string{testsupport.MustID(t, source.TypeRSS, "test-feed").String()}
	reopened := decode[reviewOpen](t, f.mustRun("cycle.review_open", `{}`))
	if reopened.SessionID != opened.SessionID || reopened.Total != 0 {
		t.Fatalf("reopened = %+v", reopened)
	}
	if !strings.Contains(f.log.String(), "excluded 3 source-policy article(s) from manual review") {
		t.Errorf("log = %s", f.log.String())
	}
}
