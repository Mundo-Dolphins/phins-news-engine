package events_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/events"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

var now = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

type fixture struct {
	t   *testing.T
	db  *sql.DB
	cfg *config.Config
	log *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "t.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Clustering.RulesPath = repofs.ExampleConfig(t, "event_clustering_rules.yml")
	cfg.Clustering.EntityCatalog.ManualPath = repofs.ExampleConfig(t, "entity_catalog_manual.yml")
	cfg.Clustering.EntityCatalog.GeneratedPath = repofs.ExampleConfig(t, "entity_catalog_generated.yml")
	cfg.Clustering.GeneratedRulesPath = filepath.Join(t.TempDir(), "generated.yml")
	return fixture{t: t, db: db, cfg: cfg, log: &bytes.Buffer{}}
}

func (f fixture) runIn(mode action.Mode, name, params string) (action.Result, error) {
	f.t.Helper()
	for _, a := range events.Actions() {
		if a.Name != name {
			continue
		}
		p, err := action.Decode(a, json.RawMessage(params))
		if err != nil {
			return action.Result{}, err
		}
		env := action.Env{DB: f.db, Config: f.cfg, Mode: mode, Now: func() time.Time { return now },
			Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: "user:ana"}
		return a.Run(context.Background(), env, p)
	}
	f.t.Fatalf("no action %q", name)
	return action.Result{}, nil
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	return f.runIn(action.ModeLocal, name, params)
}

// seed stores p (moving it to its status) and returns its id.
func (f fixture) seed(p eventruleproposal.Proposal) string {
	f.t.Helper()
	repo := sqliteeventruleproposal.NewRepository(f.db)
	p.CreatedAt = now
	got, err := repo.Upsert(context.Background(), p)
	if err != nil {
		f.t.Fatal(err)
	}
	if p.Status != eventruleproposal.StatusPending {
		got.Status = p.Status
		if err := repo.Save(context.Background(), got); err != nil {
			f.t.Fatal(err)
		}
	}
	return got.ID
}

func (f fixture) status(id string) eventruleproposal.Status {
	f.t.Helper()
	p, err := sqliteeventruleproposal.NewRepository(f.db).Get(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return p.Status
}

func typeProposal(name string, status eventruleproposal.Status) eventruleproposal.Proposal {
	return eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: name, Description: name + ".", EvidenceIDs: []string{"a1", "a2"}, Status: status, PromptVersion: "v1"}
}

func phraseProposal() eventruleproposal.Proposal {
	return eventruleproposal.Proposal{Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: "sign", Phrase: "inks a deal", Status: eventruleproposal.StatusPending, PromptVersion: "v1"}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestMetadata(t *testing.T) {
	want := map[string]struct {
		kind  action.Kind
		modes int
		long  bool
	}{
		"events.type_list": {action.KindRead, 2, false}, "events.brief": {action.KindRead, 2, false},
		"events.gate": {action.KindRead, 2, false}, "events.impact": {action.KindRead, 2, true},
		"events.type_approve": {action.KindWrite, 1, false}, "events.type_reject": {action.KindWrite, 1, false},
	}
	as := events.Actions()
	if len(as) != len(want) {
		t.Fatalf("got %d actions", len(as))
	}
	var reg action.Registry
	reg.Add(as...)
	for _, a := range as {
		w := want[a.Name]
		if a.Group != "events" || a.Kind != w.kind || len(a.Modes) != w.modes || a.LongRunning != w.long || a.Exclusive || a.Title == "" || a.Description == "" {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		props, _ := action.Schema(a.Params)["properties"].(map[string]any)
		for name, p := range props {
			if prop, _ := p.(map[string]any); prop["description"] == nil {
				t.Errorf("%s param %s has no help", a.Name, name)
			}
		}
	}
}

func TestTypeListPendingThenApproved(t *testing.T) {
	f := newFixture(t)
	f.seed(typeProposal("approved one", eventruleproposal.StatusApproved))
	f.seed(typeProposal("pending one", eventruleproposal.StatusPending))
	f.seed(phraseProposal())
	f.seed(typeProposal("rejected one", eventruleproposal.StatusRejected))
	res, err := f.runIn(action.ModeProduction, "events.type_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || res.Rows[0]["type"] != "pending one" || res.Rows[1]["type"] != "approved one" {
		t.Fatalf("rows = %v", res.Rows)
	}
	if res.Rows[0]["evidence"] != "a1, a2" || res.Rows[0]["status"] != "pending" {
		t.Errorf("row = %v", res.Rows[0])
	}
}

func TestTypeListEmpty(t *testing.T) {
	res, err := newFixture(t).run("events.type_list", `{}`)
	if err != nil || len(res.Rows) != 0 || res.Summary != "No pending or approved event type proposals." {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestBrief(t *testing.T) {
	f := newFixture(t)
	id := f.seed(typeProposal("mega trade", eventruleproposal.StatusPending))
	res, err := f.runIn(action.ModeProduction, "events.brief", `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "text" || !strings.Contains(res.Text, "mega trade") {
		t.Errorf("res = %+v", res)
	}
}

func TestBriefErrors(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("events.brief", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
	_, err = f.run("events.brief", `{}`)
	wantCode(t, err, action.CodeInvalidParams)
	_, err = f.run("events.brief", `{"id":"`+f.seed(phraseProposal())+`"}`)
	wantCode(t, err, action.CodeConflict)
	_, err = f.run("events.brief", `{"id":"`+f.seed(typeProposal("old", eventruleproposal.StatusRejected))+`"}`)
	wantCode(t, err, action.CodeConflict)
}

func TestTypeApproveAndReject(t *testing.T) {
	f := newFixture(t)
	a := f.seed(typeProposal("a", eventruleproposal.StatusPending))
	b := f.seed(typeProposal("b", eventruleproposal.StatusPending))
	res, err := f.run("events.type_approve", `{"id":"`+a+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Event type proposal " + a + " (a) approved."; res.Summary != want {
		t.Errorf("summary = %q, want %q", res.Summary, want)
	}
	res, err = f.run("events.type_reject", `{"id":"`+b+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Event type proposal " + b + " (b) rejected."; res.Summary != want {
		t.Errorf("summary = %q, want %q", res.Summary, want)
	}
	if f.status(a) != eventruleproposal.StatusApproved || f.status(b) != eventruleproposal.StatusRejected {
		t.Errorf("statuses = %s, %s", f.status(a), f.status(b))
	}
}

func TestTypeApprovePhraseProposalIsConflict(t *testing.T) {
	f := newFixture(t)
	id := f.seed(phraseProposal())
	for _, name := range []string{"events.type_approve", "events.type_reject"} {
		_, err := f.run(name, `{"id":"`+id+`"}`)
		wantCode(t, err, action.CodeConflict)
		if !strings.Contains(err.Error(), "is a phrase proposal, not a type proposal; phrases are decided in `events review`") {
			t.Errorf("%s: err = %v, want the CLI wording", name, err)
		}
		if got := f.status(id); got != eventruleproposal.StatusPending {
			t.Errorf("%s: status = %s, want the row unchanged (pending)", name, got)
		}
	}
}

func TestTypeDecideAlreadyDecidedIsConflict(t *testing.T) {
	f := newFixture(t)
	id := f.seed(typeProposal("old", eventruleproposal.StatusRejected))
	_, err := f.run("events.type_approve", `{"id":"`+id+`"}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "cannot move") {
		t.Errorf("err = %v, want the domain wording", err)
	}
	if f.status(id) != eventruleproposal.StatusRejected {
		t.Errorf("status = %s, want rejected", f.status(id))
	}
}

func TestTypeDecideMissingIsNotFound(t *testing.T) {
	_, err := newFixture(t).run("events.type_approve", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
}

const overMergeDataset = `groups:
  - id: story-a
    articles:
      - id: x1
        title: "Riley Stubbs zzqx pens a pact"
        source_id: example-sports-wire
        source_type: rss
        domain: news.example.org
        content_type: article
        published_at: "2026-03-01T09:00:00Z"
  - id: story-b
    articles:
      - id: x2
        title: "Riley Stubbs zzqx pens a pact for charity"
        source_id: example-fan-blog
        source_type: rss
        domain: blog.example.net
        content_type: article
        published_at: "2026-03-01T10:00:00Z"
`

const generatedRules = "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: zzqx pens a pact, proposal_id: evr_1, prompt_version: event-rules-v1}\n"

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGatePass(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("events.gate", `{"dataset":"`+repofs.Path(t, "testdata", "clustering_groups.yml")+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PASS" {
		t.Errorf("summary = %q", res.Summary)
	}
	b, _ := json.Marshal(res.Object)
	for _, want := range []string{`"ok":true`, `"before":{"precision"`, `"after":{"precision"`, `"dataset":"gate not exercised`, `"phrases":0`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("object = %s, missing %s", b, want)
		}
	}
}

func TestGateBlockedIsResult(t *testing.T) {
	f := newFixture(t)
	write(t, f.cfg.Clustering.GeneratedRulesPath, generatedRules)
	dataset := filepath.Join(t.TempDir(), "groups.yml")
	write(t, dataset, overMergeDataset)
	res, err := f.run("events.gate", `{"dataset":"`+dataset+`"}`)
	if err != nil {
		t.Fatalf("err = %v, want a BLOCKED result", err)
	}
	if !strings.HasPrefix(res.Summary, "BLOCKED: ") || len(res.Summary) == len("BLOCKED: ") {
		t.Errorf("summary = %q", res.Summary)
	}
	b, _ := json.Marshal(res.Object)
	if !strings.Contains(string(b), `"ok":false`) || !strings.Contains(string(b), `"dataset":"2 dataset article(s) affected"`) {
		t.Errorf("object = %s", b)
	}
}

func TestGateRefusesDatasetInProduction(t *testing.T) {
	f := newFixture(t)
	_, err := f.runIn(action.ModeProduction, "events.gate", `{"dataset":"`+repofs.Path(t, "testdata", "clustering_groups.yml")+`"}`)
	wantCode(t, err, action.CodeInvalidParams)
	if !strings.Contains(err.Error(), "dataset") {
		t.Errorf("err = %v, want it to name dataset", err)
	}
	// The configured value passes the guard; it is relative to the working
	// directory, which has no dataset here.
	_, err = f.runIn(action.ModeProduction, "events.gate", `{}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestGateEmptyRulesPathIsConflict(t *testing.T) {
	f := newFixture(t)
	f.cfg.Clustering.RulesPath = ""
	_, err := f.run("events.gate", `{}`)
	wantCode(t, err, action.CodeConflict)
}

func TestGateMissingDatasetIsNotFound(t *testing.T) {
	_, err := newFixture(t).run("events.gate", `{"dataset":"`+filepath.Join(t.TempDir(), "nope.yml")+`"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestImpactDaysMustBePositive(t *testing.T) {
	f := newFixture(t)
	for _, params := range []string{`{"days":0}`, `{"days":-3}`} {
		_, err := f.run("events.impact", params)
		wantCode(t, err, action.CodeInvalidParams)
	}
}

func TestImpactReport(t *testing.T) {
	f := newFixture(t)
	res, err := f.runIn(action.ModeProduction, "events.impact", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Object)
	for _, want := range []string{`"days":14`, `"generated_missing":true`, `"generic":0`, `"moved":[]`, `"retyped_by":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("object = %s, missing %s", b, want)
		}
	}
}

func TestImpactEmptyRulesPathIsConflict(t *testing.T) {
	f := newFixture(t)
	f.cfg.Clustering.RulesPath = ""
	_, err := f.run("events.impact", `{}`)
	wantCode(t, err, action.CodeConflict)
}
