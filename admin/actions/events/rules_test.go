package events_test

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
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/events"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// seedGeneric stores a generic article published two days before now.
func (f fixture) seedGeneric(id, title string) {
	f.t.Helper()
	ctx := context.Background()
	at := now.Add(-48 * time.Hour)
	sid, err := source.NewID(source.TypeRSS, "wire-1")
	if err != nil {
		f.t.Fatal(err)
	}
	a := article.Article{
		ID: id, OriginalURL: "https://news.example.org/" + id, CanonicalURL: "https://news.example.org/" + id,
		Source: source.Source{ID: sid, Type: source.TypeRSS}, Title: title,
		FetchedAt: at, PublishedAt: at, Status: article.StatusFetched,
	}
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, a); err != nil {
		f.t.Fatal(err)
	}
	rep := eventsignals.Representation{
		ArticleID: id, SourceID: "wire-1", Domain: "news.example.org", ContentType: article.ContentTypeArticle,
		PublishedAt: at, FetchedAt: at, EventDate: at, EventWindow: 6 * time.Hour,
		EventType: eventsignals.EventTypeGeneric,
	}
	if err := sqliteadapter.NewClusterRepository(f.db).SaveRepresentation(ctx, rep, "rules-v1", at); err != nil {
		f.t.Fatal(err)
	}
}

func rulePhrase(phrase string, evidence ...string) eventruleproposal.Proposal {
	return eventruleproposal.Proposal{
		Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: "sign",
		Phrase: phrase, EvidenceIDs: evidence, Status: eventruleproposal.StatusPending, PromptVersion: "event-rules-v1",
	}
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func list(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	l, _ := m[key].([]any)
	return l
}

type fakeLLM struct{ err error }

func (l fakeLLM) DiscoverTopics(context.Context, []appout.EventRuleArticle) ([]appout.EventTopicProposal, error) {
	return nil, l.err
}

func (l fakeLLM) DiscoverPhrases(context.Context, []appout.EventRuleArticle, []string, []string) ([]appout.EventPhraseProposal, error) {
	return nil, l.err
}

func TestRulesMetadata(t *testing.T) {
	want := map[string]struct {
		kind action.Kind
		long bool
		excl bool
	}{
		"events.rules_discover": {action.KindWrite, true, true}, "events.rules_queue": {action.KindRead, false, false},
		"events.rules_decide": {action.KindWrite, false, false}, "events.rules_reject_failing": {action.KindWrite, false, false},
		"events.rules_apply": {action.KindWrite, true, true},
	}
	found := 0
	for _, a := range events.Actions() {
		w, ok := want[a.Name]
		if !ok {
			continue
		}
		found++
		if a.Group != "events" || a.Kind != w.kind || len(a.Modes) != 1 || a.Modes[0] != action.ModeLocal || a.LongRunning != w.long || a.Exclusive != w.excl || a.Title == "" || a.Description == "" {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		props, _ := action.Schema(a.Params)["properties"].(map[string]any)
		for name, p := range props {
			if prop := obj(p); prop["description"] == nil {
				t.Errorf("%s param %s has no help", a.Name, name)
			}
		}
	}
	if found != len(want) {
		t.Fatalf("found %d of %d rule actions", found, len(want))
	}
}

func TestRulesQueueFlagsFailingAndListsNoEvidence(t *testing.T) {
	f := newFixture(t)
	f.seedGeneric("art-1", "Springfield Isotopes inks a deal with Tavion Dale")
	good := f.seed(rulePhrase("inks a deal", "art-1", "art-gone"))
	bad := f.seed(rulePhrase("xx yy", "art-1"))
	none := f.seed(rulePhrase("zzz qqq wwww", "art-1"))
	appr := rulePhrase("signs on", "art-1")
	appr.Status = eventruleproposal.StatusApproved
	approved := f.seed(appr)

	res, err := f.run("events.rules_queue", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, res.Object)
	if n, _ := m["sampled"].(float64); n != 1 {
		t.Errorf("sampled = %v", m["sampled"])
	}
	items := list(t, m, "items")
	byID := map[string]map[string]any{}
	for _, it := range items {
		im := obj(it)
		id, _ := im["id"].(string)
		byID[id] = im
	}
	if len(items) != 2 || byID[good] == nil || byID[bad] == nil {
		t.Fatalf("items = %v", items)
	}
	if byID[good]["passes"] != true || byID[bad]["passes"] != false || len(list(t, byID[bad], "reasons")) == 0 {
		t.Errorf("good = %v, bad = %v", byID[good], byID[bad])
	}
	if byID[good]["prompt_version"] != "event-rules-v1" || byID[good]["target_kind"] != "action" || byID[good]["target"] != "sign" {
		t.Errorf("good = %v", byID[good])
	}
	ev := list(t, byID[good], "evidence")
	if len(ev) != 2 {
		t.Fatalf("evidence = %v", ev)
	}
	first, second := obj(ev[0]), obj(ev[1])
	if first["article_id"] != "art-1" || first["url"] != "https://news.example.org/art-1" || first["title"] != "Springfield Isotopes inks a deal with Tavion Dale" {
		t.Errorf("first evidence = %v", first)
	}
	if second["article_id"] != "art-gone" || second["url"] != "" {
		t.Errorf("purged evidence = %v", second)
	}
	imp := obj(byID[good]["impact"])
	if len(list(t, imp, "moved")) == 0 || len(list(t, imp, "examples")) == 0 {
		t.Errorf("impact = %v", imp)
	}
	noEv := list(t, m, "no_evidence")
	if len(noEv) != 1 || obj(noEv[0])["id"] != none || obj(noEv[0])["phrase"] != "zzz qqq wwww" {
		t.Errorf("no_evidence = %v", noEv)
	}
	ap := list(t, m, "approved")
	if len(ap) != 1 || obj(ap[0])["id"] != approved || obj(ap[0])["decided_at"] == nil {
		t.Errorf("approved = %v", ap)
	}
	// Nothing was decided.
	for _, id := range []string{good, bad, none} {
		if f.status(id) != eventruleproposal.StatusPending {
			t.Errorf("%s was decided by a read", id)
		}
	}
}

func TestRulesDecideAcceptFailingIsConflict(t *testing.T) {
	f := newFixture(t)
	f.seedGeneric("art-1", "Springfield Isotopes inks a deal with Tavion Dale")
	bad := f.seed(rulePhrase("xx yy", "art-1"))
	_, err := f.run("events.rules_decide", `{"id":"`+bad+`","decision":"accept"}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "shorter than") {
		t.Errorf("err = %v, want the reasons", err)
	}
	if f.status(bad) != eventruleproposal.StatusPending {
		t.Error("failing proposal was changed")
	}
	if _, err := f.run("events.rules_decide", `{"id":"`+bad+`","decision":"reject"}`); err != nil {
		t.Fatal(err)
	}
	if f.status(bad) != eventruleproposal.StatusRejected {
		t.Error("reject did not apply")
	}
	_, err = f.run("events.rules_decide", `{"id":"`+bad+`","decision":"reject"}`)
	wantCode(t, err, action.CodeConflict)
	_, err = f.run("events.rules_decide", `{"id":"nope","decision":"reject"}`)
	wantCode(t, err, action.CodeNotFound)
	_, err = f.run("events.rules_decide", `{"id":"x","decision":"bogus"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestRulesDecideAcceptPassing(t *testing.T) {
	f := newFixture(t)
	f.seedGeneric("art-1", "Springfield Isotopes inks a deal with Tavion Dale")
	good := f.seed(rulePhrase("inks a deal", "art-1"))
	res, err := f.run("events.rules_decide", `{"id":"`+good+`","decision":"accept"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.status(good) != eventruleproposal.StatusApproved || res.Summary == "" {
		t.Errorf("status = %s, res = %+v", f.status(good), res)
	}
}

func TestRulesDecideEditPreviewAndSave(t *testing.T) {
	f := newFixture(t)
	f.seedGeneric("art-1", "Springfield Isotopes inks a deal with Tavion Dale")
	orig := f.seed(rulePhrase("inks a deal", "art-1"))

	res, err := f.run("events.rules_decide", `{"id":"`+orig+`","decision":"edit","phrase":"inks a deal with","preview":true}`)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, res.Object)
	if m["passes"] != true || m["phrase"] != "inks a deal with" {
		t.Errorf("preview = %v", m)
	}
	if f.status(orig) != eventruleproposal.StatusPending {
		t.Error("preview changed the original")
	}

	_, err = f.run("events.rules_decide", `{"id":"`+orig+`","decision":"edit","phrase":"inks a deal"}`)
	wantCode(t, err, action.CodeInvalidParams)
	_, err = f.run("events.rules_decide", `{"id":"`+orig+`","decision":"edit","phrase":"never cited anywhere"}`)
	wantCode(t, err, action.CodeInvalidParams)
	_, err = f.run("events.rules_decide", `{"id":"`+orig+`","decision":"edit","phrase":"deal"}`)
	wantCode(t, err, action.CodeConflict)

	res, err = f.run("events.rules_decide", `{"id":"`+orig+`","decision":"edit","phrase":" Inks A Deal With "}`)
	if err != nil {
		t.Fatal(err)
	}
	m = asMap(t, res.Object)
	newID, _ := m["id"].(string)
	if newID == "" || newID == orig || m["passes"] != true {
		t.Fatalf("save = %v", m)
	}
	if f.status(newID) != eventruleproposal.StatusApproved || f.status(orig) != eventruleproposal.StatusRejected {
		t.Errorf("new = %s, original = %s", f.status(newID), f.status(orig))
	}
	_, err = f.run("events.rules_decide", `{"id":"`+orig+`","decision":"edit","phrase":"inks a deal with tavion"}`)
	wantCode(t, err, action.CodeConflict)
}

func TestRulesRejectFailingOnlyPendingFailing(t *testing.T) {
	f := newFixture(t)
	f.seedGeneric("art-1", "Springfield Isotopes inks a deal with Tavion Dale")
	good := f.seed(rulePhrase("inks a deal", "art-1"))
	bad := f.seed(rulePhrase("xx yy", "art-1"))
	none := f.seed(rulePhrase("zzz qqq wwww", "art-1"))
	badApproved := rulePhrase("ab cd", "art-1")
	badApproved.Status = eventruleproposal.StatusApproved
	approved := f.seed(badApproved)

	res, err := f.run("events.rules_reject_failing", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	rejected := list(t, asMap(t, res.Object), "rejected")
	if len(rejected) != 1 || obj(rejected[0])["id"] != bad || len(list(t, obj(rejected[0]), "reasons")) == 0 {
		t.Fatalf("rejected = %v", rejected)
	}
	want := map[string]eventruleproposal.Status{
		good: eventruleproposal.StatusPending, bad: eventruleproposal.StatusRejected,
		none: eventruleproposal.StatusPending, approved: eventruleproposal.StatusApproved,
	}
	for id, st := range want {
		if f.status(id) != st {
			t.Errorf("%s = %s, want %s", id, f.status(id), st)
		}
	}
	res, err = f.run("events.rules_reject_failing", `{}`)
	if err != nil || len(list(t, asMap(t, res.Object), "rejected")) != 0 {
		t.Fatalf("second run: %+v, %v", res, err)
	}
}

func TestRulesApplyNothing(t *testing.T) {
	f := newFixture(t)
	defer events.SetGateDataset(filepath.Join(t.TempDir(), "missing.yml"))()
	res, err := f.run("events.rules_apply", `{}`)
	if err != nil {
		t.Fatalf("nothing to apply must not run the gate: %v", err)
	}
	if res.Summary != "Nothing to apply." || asMap(t, res.Object)["nothing"] != true {
		t.Errorf("res = %+v", res)
	}
	if _, err := os.Stat(f.cfg.Clustering.GeneratedRulesPath); !os.IsNotExist(err) {
		t.Errorf("file written: %v", err)
	}
}

func TestRulesApplyMissingDatasetIsConflict(t *testing.T) {
	f := newFixture(t)
	defer events.SetGateDataset(filepath.Join(t.TempDir(), "missing.yml"))()
	appr := rulePhrase("inks a deal", "art-1")
	appr.Status = eventruleproposal.StatusApproved
	id := f.seed(appr)
	_, err := f.run("events.rules_apply", `{}`)
	wantCode(t, err, action.CodeConflict)
	if f.status(id) != eventruleproposal.StatusApproved {
		t.Error("proposal changed")
	}
}

func TestRulesApplyBlockedKeepsApproved(t *testing.T) {
	f := newFixture(t)
	write(t, f.cfg.Clustering.GeneratedRulesPath, generatedRules)
	dataset := filepath.Join(t.TempDir(), "groups.yml")
	write(t, dataset, overMergeDataset)
	defer events.SetGateDataset(dataset)()
	appr := rulePhrase("inks a deal", "art-1")
	appr.Status = eventruleproposal.StatusApproved
	id := f.seed(appr)

	res, err := f.run("events.rules_apply", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, res.Object)
	if m["blocked"] != true || m["reason"] == "" || !strings.HasPrefix(res.Summary, "BLOCKED: ") {
		t.Errorf("res = %+v, object = %v", res, m)
	}
	if f.status(id) != eventruleproposal.StatusApproved {
		t.Error("blocked apply changed the proposal")
	}
	got, _ := os.ReadFile(f.cfg.Clustering.GeneratedRulesPath)
	if string(got) != generatedRules {
		t.Errorf("generated file changed: %s", got)
	}
}

func TestRulesApplyPassWritesAndMarks(t *testing.T) {
	f := newFixture(t)
	defer events.SetGateDataset(repofs.Path(t, "testdata", "clustering_groups.yml"))()
	appr := rulePhrase("inks a deal", "art-1")
	appr.Status = eventruleproposal.StatusApproved
	id := f.seed(appr)

	res, err := f.run("events.rules_apply", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PASS: 1 rule(s) applied to "+f.cfg.Clustering.GeneratedRulesPath {
		t.Errorf("summary = %q", res.Summary)
	}
	m := asMap(t, res.Object)
	if len(list(t, m, "applied")) != 1 || m["before"] == nil || m["after"] == nil || m["blocked"] == true {
		t.Errorf("object = %v", m)
	}
	if f.status(id) != eventruleproposal.StatusApplied {
		t.Errorf("status = %s", f.status(id))
	}
	got, err := os.ReadFile(f.cfg.Clustering.GeneratedRulesPath)
	if err != nil || !strings.Contains(string(got), "inks a deal") {
		t.Errorf("file = %q, err = %v", got, err)
	}
}

func TestRulesDiscoverDisabledIsConflict(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("events.rules_discover", `{}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "offline_training.llm") {
		t.Errorf("err = %v", err)
	}
}

func TestRulesDiscoverAllCallsFailedIsWarning(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.BaseURL = "http://llm.invalid"
	f.cfg.OfflineTraining.LLM.Model = "m"
	f.seedGeneric("art-1", "Springfield Isotopes inks a deal with Tavion Dale")
	defer events.SetDiscoveryLLM(fakeLLM{err: errors.New("boom")})()
	res, err := f.run("events.rules_discover", `{}`)
	if err != nil {
		t.Fatalf("err = %v, want a WARNING result", err)
	}
	if !strings.HasPrefix(res.Summary, "WARNING: 1 sampled, ") || !strings.Contains(res.Summary, "(") {
		t.Errorf("summary = %q", res.Summary)
	}
	if asMap(t, res.Object)["error"] == "" {
		t.Errorf("object = %v", res.Object)
	}
}

func TestRulesDiscoverSummary(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.BaseURL = "http://llm.invalid"
	f.cfg.OfflineTraining.LLM.Model = "m"
	f.seedGeneric("art-1", "Springfield Isotopes inks a deal with Tavion Dale")
	defer events.SetDiscoveryLLM(fakeLLM{})()
	res, err := f.run("events.rules_discover", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(res.Summary, "WARNING") || !strings.HasPrefix(res.Summary, "1 sampled, 2 calls (0 failed), 0 proposals") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestRulesQueueEmpty(t *testing.T) {
	res, err := newFixture(t).run("events.rules_queue", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, res.Object)
	if len(list(t, m, "items")) != 0 || len(list(t, m, "no_evidence")) != 0 || len(list(t, m, "approved")) != 0 || m["sampled"] != float64(0) {
		t.Errorf("object = %v", m)
	}
}
