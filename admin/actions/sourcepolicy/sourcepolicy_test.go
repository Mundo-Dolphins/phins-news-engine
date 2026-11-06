package sourcepolicy_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const policyYAML = `
version: 1
mode: shadow
policies:
  - source_id: rss:ghost
    authority: official_team
    retain_near_duplicates: true
    representative_preference: high
    allow_official_confirmation: true
    allow_substantial_update: true
    publish_confirmation_without_new_information: false
    minimum_update_novelty: 0.65
    event_cooldown: 20m
`

type fixture struct {
	t   *testing.T
	db  *sql.DB
	cfg *config.Config
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	cfg, err := config.Load(testsupport.WriteFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.Fetch.SourcesPath = filepath.Join(dir, "sources.yml")
	cfg.SourcePolicy.Path = filepath.Join(dir, "source-policies.yml")
	if err := os.WriteFile(cfg.Fetch.SourcesPath, []byte("websites: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.SourcePolicy.Path, []byte(policyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := infrasqlite.Open(filepath.Join(dir, "op.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return fixture{t, db, cfg}
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range sourcepolicy.Actions() {
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
	env := action.Env{DB: f.db, Config: f.cfg, Mode: action.ModeProduction, Now: time.Now,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Actor: "user:javi"}
	return a.Run(context.Background(), env, p)
}

func (f fixture) saveArticle(id string) {
	f.t.Helper()
	src := source.Source{ID: testsupport.MustID(f.t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	now := time.Now().UTC()
	if err := sqliteadapter.NewArticleRepository(f.db).Save(context.Background(), article.Article{
		ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
	}); err != nil {
		f.t.Fatal(err)
	}
}

func wantCode(t *testing.T, err error, code string) *action.Error {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
	return ae
}

func TestMetadata(t *testing.T) {
	names := map[string]bool{}
	for _, a := range sourcepolicy.Actions() {
		names[a.Name] = true
		if a.Kind != action.KindRead {
			t.Errorf("%s kind = %v, want read", a.Name, a.Kind)
		}
		if len(a.Modes) != 2 {
			t.Errorf("%s modes = %v, want both", a.Name, a.Modes)
		}
	}
	for _, n := range []string{"source_policy.validate", "source_policy.inspect", "source_policy.audit", "source_policy.simulate"} {
		if !names[n] {
			t.Errorf("missing action %s", n)
		}
	}
}

func TestValidateReportsIssues(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("source_policy.validate", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0]["issue"] == "" {
		t.Fatalf("rows = %v, want 1 issue", res.Rows)
	}
	if res.Summary != "1 issue(s)" {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestValidateOK(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.cfg.SourcePolicy.Path, []byte("version: 1\nmode: shadow\npolicies: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("source_policy.validate", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.Summary != "OK" {
		t.Fatalf("res = %+v", res)
	}
}

func TestInspectFields(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("source_policy.inspect", `{"source":"rss:ghost"}`)
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := res.Object.(map[string]any)
	if res.Render != "object" || !ok {
		t.Fatalf("res = %+v", res)
	}
	for _, k := range []string{"source_id", "authority", "mode", "retain_near_duplicates", "representative_preference",
		"allow_official_confirmation", "allow_substantial_update", "publish_confirmation_without_new_information",
		"minimum_update_novelty", "event_cooldown"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
	if obj["authority"] != "official_team" || obj["mode"] != "shadow" || obj["event_cooldown"] != "20m0s" {
		t.Errorf("obj = %v", obj)
	}
}

func TestAuditBadDate(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("source_policy.audit", `{"from":"2026-13-01","to":"2026-12-01"}`)
	ae := wantCode(t, err, action.CodeInvalidParams)
	if !bytes.Contains([]byte(ae.Message), []byte("from")) {
		t.Errorf("message %q does not name from", ae.Message)
	}
}

func TestAuditRenamesEmptyCooldown(t *testing.T) {
	f := newFixture(t)
	f.saveArticle("a1")
	repo := sqliteadapter.NewSourcePolicyRepository(f.db)
	if err := repo.Save(context.Background(), out.SourcePolicyDecisionRecord{
		ID: "d1", ArticleID: "a1", SourceID: "rss:feed-a", DecisionCode: "x", Mode: "shadow",
		DecidedAt: time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("source_policy.audit", `{"from":"2026-06-01","to":"2026-07-01"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Object)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	cool, _ := m["by_cooldown_outcome"].(map[string]any)
	if cool["not_applicable"] != float64(1) {
		t.Fatalf("object = %s", b)
	}
	if _, bad := cool[""]; bad {
		t.Errorf("empty key not renamed: %s", b)
	}
	if m["total_decisions"] != float64(1) {
		t.Errorf("object = %s", b)
	}
}

func TestSimulateNoDecisions(t *testing.T) {
	f := newFixture(t)
	f.saveArticle("a1")
	res, err := f.run("source_policy.simulate", `{"article_id":"a1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "table" || len(res.Rows) != 0 || res.Summary != "article a1 (source rss:feed-a)" {
		t.Fatalf("res = %+v", res)
	}
}

func TestSimulateUnknownArticleIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("source_policy.simulate", `{"article_id":"nope"}`)
	_ = wantCode(t, err, action.CodeNotFound)
}
