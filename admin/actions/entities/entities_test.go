package entities_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/entities"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

const dictionaryYAML = `
meta:
  version: "1.0.0"
  name: isotopes
  language: en
decision:
  positive_threshold_high: 0.8
  positive_threshold_medium: 0.5
  negative_threshold_high: 0
  title_weight_multiplier: 1.5
  max_count_per_term: 5
categories:
  active_roster_core:
    weight: 1
    entries: []
`

const manualYAML = `entities:
  - id: springfield-isotopes
    canonical_name: Springfield Isotopes
    kind: team
    terms: [isotopes, springfield isotopes]
    is_primary_team: true
  - id: jordan-sample
    canonical_name: Jordan Sample
    kind: player
    terms: [Jordan Sample, Sample]
`

type fixture struct {
	t                       *testing.T
	db                      *sql.DB
	cfg                     *config.Config
	dict, manual, generated string
	log                     *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	dir := t.TempDir()
	f := fixture{t: t, db: db, cfg: cfg, log: &bytes.Buffer{},
		dict: filepath.Join(dir, "dictionary.yml"), manual: filepath.Join(dir, "manual.yml"), generated: filepath.Join(dir, "generated.yml")}
	write(t, f.dict, dictionaryYAML)
	write(t, f.manual, manualYAML)
	write(t, f.generated, "entities: []\n")
	cfg.Dictionary.Path = f.dict
	cfg.Clustering.EntityCatalog.ManualPath = f.manual
	cfg.Clustering.EntityCatalog.GeneratedPath = f.generated
	cfg.Clustering.RulesPath = repofs.ExampleConfig(t, "event_clustering_rules.yml")
	cfg.Clustering.GeneratedRulesPath = ""
	return f
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	return f.runIn(action.ModeLocal, name, params)
}

func (f fixture) runIn(mode action.Mode, name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range entities.Actions() {
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
	env := action.Env{DB: f.db, Config: f.cfg, Mode: mode, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: "user:ana"}
	return a.Run(context.Background(), env, p)
}

func (f fixture) seed(p entityproposal.Proposal) string {
	f.t.Helper()
	if p.NormalizedName == "" {
		p.NormalizedName = entityproposal.Normalize(p.CanonicalName)
	}
	if p.EntityType == "" {
		p.EntityType = entityproposal.EntityTypePlayer
	}
	if p.Kind == "" {
		p.Kind = entityproposal.KindNewEntity
	}
	status := p.Status
	p.Status = entityproposal.StatusPending
	p.FirstSeen, p.LastSeen, p.CreatedAt, p.ExtractorVersion = now, now, now, "v1"
	repo := sqliteentityproposal.NewEntityProposalRepository(f.db)
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		f.t.Fatalf("upsert %s: %v", p.CanonicalName, err)
	}
	if status != "" && status != entityproposal.StatusPending {
		if err := repo.UpdateStatus(context.Background(), id, status, now); err != nil {
			f.t.Fatalf("update status: %v", err)
		}
	}
	return id
}

func (f fixture) status(id string) entityproposal.Status {
	f.t.Helper()
	p, err := sqliteentityproposal.NewEntityProposalRepository(f.db).Get(context.Background(), id)
	if err != nil {
		f.t.Fatalf("get %s: %v", id, err)
	}
	return p.Status
}

func (f fixture) proposalCount() int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM entity_proposals`).Scan(&n); err != nil {
		f.t.Fatalf("count: %v", err)
	}
	return n
}

func hash(t *testing.T, paths ...string) [32]byte {
	t.Helper()
	h := sha256.New()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		h.Write(b)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestMetadata(t *testing.T) {
	type want struct {
		kind        action.Kind
		modes       int
		longRunning bool
		exclusive   bool
	}
	cases := map[string]want{
		"entities.list":              {action.KindRead, 2, false, false},
		"entities.show":              {action.KindRead, 2, false, false},
		"entities.approve":           {action.KindWrite, 1, false, false},
		"entities.reject":            {action.KindWrite, 1, false, false},
		"entities.generate":          {action.KindWrite, 1, false, false},
		"entities.discover":          {action.KindWrite, 1, true, true},
		"entities.proposals_verify":  {action.KindWrite, 1, true, true},
		"entities.extract":           {action.KindRead, 2, false, false},
		"entities.fallback_test":     {action.KindRead, 1, false, false},
		"entities.league_evaluate":   {action.KindRead, 2, false, false},
		"entities.roster_sync":       {action.KindWrite, 1, false, false},
		"entities.wikidata_evaluate": {action.KindRead, 1, true, false},
		"entities.review_queue":      {action.KindRead, 1, false, false},
		"entities.catalog_search":    {action.KindRead, 1, false, false},
	}
	as := entities.Actions()
	if len(as) != len(cases) {
		t.Fatalf("got %d actions, want %d", len(as), len(cases))
	}
	var reg action.Registry
	reg.Add(as...) // panics on an invalid params struct
	for _, a := range as {
		w, ok := cases[a.Name]
		if !ok {
			t.Fatalf("unexpected action %s", a.Name)
		}
		if a.Group != "entities" || a.Kind != w.kind || len(a.Modes) != w.modes || a.LongRunning != w.longRunning || a.Exclusive != w.exclusive {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		if w.modes == 1 && a.Modes[0] != action.ModeLocal {
			t.Errorf("%s must be local-only", a.Name)
		}
		if a.Title == "" || a.Description == "" {
			t.Errorf("%s has no title or description", a.Name)
		}
		if a.Params != nil {
			props, _ := action.Schema(a.Params)["properties"].(map[string]any)
			for name, p := range props {
				if prop, _ := p.(map[string]any); prop["description"] == nil {
					t.Errorf("%s param %s has no help", a.Name, name)
				}
			}
		}
	}
}

func TestListSortedByScore(t *testing.T) {
	f := newFixture(t)
	low := f.seed(entityproposal.Proposal{CanonicalName: "Quill Farrowby", Occurrences: 1, Confidence: 0.5})
	high := f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Occurrences: 9, Confidence: 0.9})
	f.seed(entityproposal.Proposal{CanonicalName: "Orrin Pellwick", Occurrences: 20, Status: entityproposal.StatusRejected})
	res, err := f.run("entities.list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || res.Rows[0]["id"] != high || res.Rows[1]["id"] != low {
		t.Fatalf("rows = %+v, want %s then %s", res.Rows, high, low)
	}
	if res.Rows[0]["confidence"] != "0.90" || res.Rows[0]["name"] != "Zeno Ashbury" || res.Rows[0]["status"] != "pending" {
		t.Fatalf("row = %+v", res.Rows[0])
	}
	all, err := f.run("entities.list", `{"status":"all"}`)
	if err != nil || len(all.Rows) != 3 || all.Rows[0]["name"] != "Orrin Pellwick" {
		t.Fatalf("all = %+v, %v", all.Rows, err)
	}
}

func TestShowUnknownIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("entities.show", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)

	id := f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Aliases: []string{"Ashbury"},
		Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "Zeno Ashbury signs"}}})
	res, err := f.run("entities.show", `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Object)
	if res.Render != "object" || !strings.Contains(string(b), "Zeno Ashbury signs") || !strings.Contains(string(b), `"Ashbury"`) {
		t.Fatalf("object = %s", b)
	}
}

func TestApproveNotPendingIsConflictAndFilesUntouched(t *testing.T) {
	f := newFixture(t)
	id := f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Status: entityproposal.StatusRejected})
	before := hash(t, f.manual, f.dict)
	_, err := f.run("entities.approve", `{"id":"`+id+`"}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "is already rejected") {
		t.Fatalf("err = %v", err)
	}
	if hash(t, f.manual, f.dict) != before {
		t.Fatal("catalog files changed")
	}
	if f.status(id) != entityproposal.StatusRejected {
		t.Fatal("status changed")
	}
	_, err = f.run("entities.approve", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestApproveNewPlayer(t *testing.T) {
	f := newFixture(t)
	id := f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Confidence: 0.9, Occurrences: 3,
		Metadata: map[string]string{"position": "LB", "affiliation_status": "team", "team_id": "springfield-isotopes"}})
	res, err := f.run("entities.approve", `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "approved Zeno Ashbury (" + id + "); run entities.generate to regenerate the deterministic entity catalog"
	if res.Text != want {
		t.Fatalf("text = %q, want %q", res.Text, want)
	}
	if f.status(id) != entityproposal.StatusApproved {
		t.Fatal("not approved")
	}
	if b, _ := os.ReadFile(f.dict); !strings.Contains(string(b), "Zeno Ashbury") {
		t.Fatalf("dictionary lacks the player:\n%s", b)
	}
}

func TestApprovePossibleDuplicateNeedsResolutionIsConflict(t *testing.T) {
	f := newFixture(t)
	id := f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Kind: entityproposal.KindPossibleDuplicate})
	before := hash(t, f.manual, f.dict)
	_, err := f.run("entities.approve", `{"id":"`+id+`"}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "merge_into") || !strings.Contains(err.Error(), "as_new") || strings.Contains(err.Error(), "--") {
		t.Fatalf("err = %v, want the panel param names", err)
	}
	if hash(t, f.manual, f.dict) != before || f.status(id) != entityproposal.StatusPending {
		t.Fatal("files or status changed")
	}
}

func TestApproveBothResolutionsIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	id := f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Kind: entityproposal.KindPossibleDuplicate})
	_, err := f.run("entities.approve", `{"id":"`+id+`","merge_into":"jordan-sample","as_new":true}`)
	wantCode(t, err, action.CodeInvalidParams)
	if err.Error() != "pass only one of merge_into or as_new" {
		t.Fatalf("err = %v", err)
	}
	if f.status(id) != entityproposal.StatusPending {
		t.Fatal("status changed")
	}
}

func TestRejectThenRejectAgainIsConflict(t *testing.T) {
	f := newFixture(t)
	id := f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury"})
	res, err := f.run("entities.reject", `{"id":"`+id+`"}`)
	if err != nil || res.Text != "rejected Zeno Ashbury ("+id+")" {
		t.Fatalf("reject = %+v, %v", res, err)
	}
	_, err = f.run("entities.reject", `{"id":"`+id+`"}`)
	wantCode(t, err, action.CodeConflict)
	_, err = f.run("entities.reject", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestDiscoverBadSinceIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury"})
	before := f.proposalCount()
	for _, since := range []string{"7x", "-3d", "2026-13-01"} {
		_, err := f.run("entities.discover", `{"since":"`+since+`"}`)
		wantCode(t, err, action.CodeInvalidParams)
		if !strings.HasPrefix(err.Error(), "since: ") {
			t.Fatalf("err = %v, want it to name since", err)
		}
	}
	if f.proposalCount() != before {
		t.Fatal("proposals changed")
	}
}

func TestDiscoverFailedBatchesWarns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(t, source.TypeWeb, "example-com"), Name: "Example", Type: source.TypeWeb}
	for _, a := range []struct{ id, title string }{
		{"a1", "Isotopes sign CB Jai'Rene' Mossbank"},
		{"a2", "Sharks claim LB Quentin Halloway"},
	} {
		if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
			ID: a.id, OriginalURL: "https://example.com/" + a.id, CanonicalURL: "https://example.com/" + a.id,
			Source: src, Title: a.title, Summary: a.title, FetchedAt: now, Status: article.StatusFetched,
		}); err != nil {
			t.Fatalf("save article: %v", err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Mossbank") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"entities":[{"name":"Quentin Halloway","type":"player","aliases":["Halloway"],"metadata":{"position":"LB"},"confidence":0.9}]}`,
		}}}})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	f.cfg.EntityDiscovery = config.EntityDiscoveryConfig{
		Enabled: true, ExtractorVersion: "v1", BatchSize: 1,
		LLM: config.EntityDiscoveryLLMConfig{BaseURL: srv.URL, Model: "test-model", TimeoutSeconds: 5},
	}
	res, err := f.run("entities.discover", `{"all":true,"offline":true}`)
	if err != nil {
		t.Fatalf("discover error = %v, want a result", err)
	}
	if !strings.HasPrefix(res.Summary, "WARNING: 1 of 2 batches failed; 1 proposals, 0 auto-rejected, 0 uncertain") {
		t.Fatalf("summary = %q", res.Summary)
	}
	if !strings.Contains(f.log.String(), "level=WARN") {
		t.Fatalf("log lacks the batch warning:\n%s", f.log)
	}
	if f.proposalCount() != 1 {
		t.Fatal("the successful batch's proposal is not saved")
	}
}

func TestExtractNoTextIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	// Missing config files prove nothing is read before the text check.
	f.cfg.Clustering.RulesPath = filepath.Join(t.TempDir(), "missing.yml")
	_, err := f.run("entities.extract", `{"title":"  ","description":"\n","body":"\t"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestExtractFindsCatalogEntity(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("entities.extract", `{"title":"Isotopes sign Random Newcomer as Jordan Sample watches"}`)
	if err != nil {
		t.Fatal(err)
	}
	rep, ok := res.Object.(eventsignals.Representation)
	if !ok || rep.PrimaryEntity == "" {
		t.Fatalf("object = %#v", res.Object)
	}
	if !strings.HasPrefix(res.Summary, "primary=") || !strings.Contains(res.Summary, "team=springfield-isotopes") && !strings.Contains(res.Summary, "team=Springfield Isotopes") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestLeagueEvaluateDisabledIsConflict(t *testing.T) {
	f := newFixture(t)
	f.cfg.EntityDiscovery.League.Enabled = false
	_, err := f.run("entities.league_evaluate", `{}`)
	wantCode(t, err, action.CodeConflict)
}

func TestGenerateWritesConfiguredPath(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("entities.generate", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "wrote 0 entities to "+f.generated {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestApproveConcurrentCallsBothLand(t *testing.T) {
	f := newFixture(t)
	ids := []string{
		f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Confidence: 0.9, Occurrences: 3,
			Metadata: map[string]string{"position": "LB", "affiliation_status": "team", "team_id": "springfield-isotopes"}}),
		f.seed(entityproposal.Proposal{CanonicalName: "Orrin Tellwater", Confidence: 0.9, Occurrences: 3,
			Metadata: map[string]string{"position": "QB", "affiliation_status": "team", "team_id": "springfield-isotopes"}}),
	}
	var wg sync.WaitGroup
	errs := make([]error, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.run("entities.approve", `{"id":"`+id+`"}`)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("approve %s: %v", ids[i], err)
		}
		if f.status(ids[i]) != entityproposal.StatusApproved {
			t.Fatalf("%s not approved", ids[i])
		}
	}
	b, err := os.ReadFile(f.dict)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Zeno Ashbury", "Orrin Tellwater"} {
		if !strings.Contains(string(b), name) {
			t.Fatalf("dictionary lacks %s (lost update):\n%s", name, b)
		}
	}
}

func TestExtractProductionRefusesPathOverrides(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yml")
	for _, field := range []string{"rules", "manual", "generated", "generated_rules", "roster", "league_rules"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.runIn(action.ModeProduction, "entities.extract",
				`{"title":"Isotopes win","league":true,"`+field+`":"`+missing+`"}`)
			var ae *action.Error
			if !errors.As(err, &ae) || ae.Code != action.CodeInvalidParams || ae.Message != field+" cannot be changed in production" {
				t.Fatalf("err = %v, want the production refusal for %s", err, field)
			}
		})
	}
}

func TestExtractProductionUsesConfiguredPaths(t *testing.T) {
	f := newFixture(t)
	if _, err := f.runIn(action.ModeProduction, "entities.extract",
		`{"title":"Isotopes win","manual":"`+f.manual+`"}`); err != nil {
		t.Fatalf("configured paths refused in production: %v", err)
	}
}

func TestLeagueEvaluateProductionRefusesPathOverrides(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yml")
	for _, field := range []string{"manual", "generated"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			f.cfg.EntityDiscovery.League.Enabled = true
			_, err := f.runIn(action.ModeProduction, "entities.league_evaluate", `{"`+field+`":"`+missing+`"}`)
			var ae *action.Error
			if !errors.As(err, &ae) || ae.Code != action.CodeInvalidParams || ae.Message != field+" cannot be changed in production" {
				t.Fatalf("err = %v, want the production refusal for %s", err, field)
			}
		})
	}
}
