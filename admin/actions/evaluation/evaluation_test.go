package evaluation_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/evaluation"
	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguebacktest"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/evalwiring"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

const clusteringDataset = `
groups:
  - id: g1
    representative: a1
    articles:
      - id: a1
        title: "Isotopes Sign Veteran Linebacker"
        source_id: src-1
        content_type: article
        published_at: "2026-07-01T10:00:00Z"
        expected_role: first_report
      - id: a2
        title: "Isotopes Signed Veteran Linebacker, Outlet Reports"
        source_id: src-2
        content_type: article
        published_at: "2026-07-01T11:00:00Z"
        expected_role: confirmation
`

const dedupDataset = `
pairs:
  - a: {title: "Isotopes Beat Sharks In Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:00:00Z"}
    b: {title: "Isotopes Beat Sharks In An Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:05:00Z"}
    label: same_content
  - a: {title: "Isotopes Beat Sharks In Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:00:00Z"}
    b: {title: "Weather Forecast Calls For Rain This Weekend", source_type: rss, content_type: article, published_at: "2026-07-01T08:00:00Z"}
    label: different
  - a: {title: "Isotopes Sign Veteran Linebacker", source_type: rss, content_type: article, published_at: "2026-07-01T10:00:00Z"}
    b: {title: "Isotopes Sign Linebacker", source_type: rss, content_type: article, published_at: "2026-07-01T10:05:00Z"}
    label: ambiguous
`

const roster = "full_name,first_name,last_name,football_name,team,position,status,gsis_id,week\nAvery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5\n"

type fixture struct {
	t   *testing.T
	cfg *config.Config
	log *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "operational.db")
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	cfg := &config.Config{}
	cfg.SQLite.Path = dbPath
	cfg.Clustering.RulesPath = repofs.ExampleConfig(t, "event_clustering_rules.yml")
	cfg.Clustering.EntityCatalog.ManualPath = repofs.ExampleConfig(t, "entity_catalog_manual.yml")
	cfg.Clustering.EntityCatalog.GeneratedPath = repofs.ExampleConfig(t, "entity_catalog_generated.yml")
	nd := &cfg.Deduplication.NearDuplicate
	nd.Method = "jaccard"
	nd.DefaultThreshold = 0.82
	nd.ShortTextMaxTokens = 8
	nd.ShortTextThreshold = 0.90
	cfg.Clustering.EventThreshold = 0.55
	return fixture{t: t, cfg: cfg, log: &bytes.Buffer{}}
}

func (f fixture) file(name, content string) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f fixture) runIn(mode action.Mode, name, params string) (action.Result, error) {
	f.t.Helper()
	for _, a := range evaluation.Actions() {
		if a.Name != name {
			continue
		}
		p, err := action.Decode(a, json.RawMessage(params))
		if err != nil {
			return action.Result{}, err
		}
		env := action.Env{Config: f.cfg, Mode: mode, Now: time.Now, ConfigPath: "unused.yml",
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

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func jsonOf(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMetadata(t *testing.T) {
	want := map[string]struct {
		group string
		kind  action.Kind
		modes int
	}{
		"clustering.evaluate": {"clustering", action.KindRead, 2},
		"dedup.evaluate":      {"dedup", action.KindRead, 2},
		"backtest.candidate":  {"backtest", action.KindWrite, 1},
		"backtest.league":     {"backtest", action.KindRead, 1},
	}
	as := evaluation.Actions()
	if len(as) != len(want) {
		t.Fatalf("got %d actions", len(as))
	}
	var reg action.Registry
	reg.Add(as...)
	for _, a := range as {
		w := want[a.Name]
		if a.Group != w.group || a.Kind != w.kind || len(a.Modes) != w.modes || !a.LongRunning || a.Exclusive || a.Title == "" || a.Description == "" {
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

func TestClusteringEvaluate(t *testing.T) {
	f := newFixture(t)
	res, err := f.runIn(action.ModeLocal, "clustering.evaluate", `{"dataset":`+quote(f.file("g.yml", clusteringDataset))+`}`)
	if err != nil {
		t.Fatal(err)
	}
	obj := jsonOf(t, res.Object)
	if res.Render != "object" || obj["articles_total"] != float64(2) {
		t.Fatalf("res = %+v obj = %v", res, obj)
	}
	if as, _ := obj["assignments"].([]any); len(as) != 2 {
		t.Errorf("assignments = %v, want 2", obj["assignments"])
	}
	if _, ok := obj["pairwise"].(map[string]any); !ok {
		t.Errorf("pairwise = %v", obj["pairwise"])
	}
	if _, ok := obj["role_accuracy"]; !ok {
		t.Errorf("role_accuracy missing although the dataset declares roles")
	}
}

func TestClusteringEvaluateMissingDatasetIsNotFound(t *testing.T) {
	_, err := newFixture(t).run("clustering.evaluate", `{"dataset":"/nonexistent/groups.yml"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestClusteringEvaluateRefusesGeneratedRulesInProduction(t *testing.T) {
	f := newFixture(t)
	f.cfg.Clustering.GeneratedRulesPath = f.file("generated.yml", "meta: {version: 1}\n")
	other := f.file("other.yml", "meta: {version: 1}\n")
	_, err := f.runIn(action.ModeProduction, "clustering.evaluate", `{"generated_rules":`+quote(other)+`}`)
	wantCode(t, err, action.CodeInvalidParams)
	if !strings.Contains(err.Error(), "generated_rules") {
		t.Errorf("err = %v, want it to name generated_rules", err)
	}
	// The dataset is refused too.
	_, err = f.runIn(action.ModeProduction, "clustering.evaluate", `{"dataset":"elsewhere.yml"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestClusteringEvaluateAcceptsConfiguredGeneratedRulesInProduction(t *testing.T) {
	f := newFixture(t)
	f.cfg.Clustering.GeneratedRulesPath = f.file("generated.yml", "meta: {version: 1}\n")
	_, err := f.runIn(action.ModeProduction, "clustering.evaluate", `{"generated_rules":`+quote(f.cfg.Clustering.GeneratedRulesPath)+`}`)
	// Params are accepted; the default dataset is relative to the repo root,
	// so the run itself may not find it from the package directory.
	var ae *action.Error
	if errors.As(err, &ae) && ae.Code == action.CodeInvalidParams {
		t.Fatalf("configured value refused: %v", err)
	}
}

func TestDedupEvaluate(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("dedup.evaluate", `{"dataset":`+quote(f.file("p.yml", dedupDataset))+`}`)
	if err != nil {
		t.Fatal(err)
	}
	obj := jsonOf(t, res.Object)
	if obj["evaluated"] != float64(2) || obj["excluded"] != float64(1) {
		t.Fatalf("obj = %v", obj)
	}
	bySrc, _ := obj["by_source_type"].([]any)
	if len(bySrc) != 1 || asMap(bySrc[0])["key"] != "rss" {
		t.Errorf("by_source_type = %v", obj["by_source_type"])
	}
	conf, _ := obj["confusion"].([]any)
	if len(conf) == 0 {
		t.Fatalf("confusion = %v", obj["confusion"])
	}
	prev := ""
	for _, r := range conf {
		row := asMap(r)
		cur := fmt.Sprint(row["predicted"], "/", row["gold"])
		if cur < prev {
			t.Errorf("confusion rows not sorted: %v", conf)
		}
		prev = cur
	}
}

func TestDedupEvaluateMissingDatasetIsNotFound(t *testing.T) {
	_, err := newFixture(t).run("dedup.evaluate", `{"dataset":"/nonexistent/pairs.yml"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestDedupEvaluateRefusesDatasetInProduction(t *testing.T) {
	_, err := newFixture(t).runIn(action.ModeProduction, "dedup.evaluate", `{"dataset":"elsewhere.yml"}`)
	wantCode(t, err, action.CodeInvalidParams)
	if !strings.Contains(err.Error(), "dataset") {
		t.Errorf("err = %v, want it to name dataset", err)
	}
}

func TestCandidateNeedsACandidate(t *testing.T) {
	_, err := newFixture(t).run("backtest.candidate", `{}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestCandidateDaysMustBePositive(t *testing.T) {
	_, err := newFixture(t).run("backtest.candidate", `{"days":0,"candidate_dictionary":"x.yml"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestLeagueDaysMustBePositive(t *testing.T) {
	_, err := newFixture(t).run("backtest.league", `{"days":-1}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestLeagueReportsOK(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("backtest.league", `{"roster":`+quote(f.file("roster.csv", roster))+`,"league_rules":`+
		quote(repofs.ExampleConfig(t, "league_alias_rules.yml"))+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "OK" || !strings.Contains(res.Text, "invariant: OK") || res.Render != "text" {
		t.Fatalf("res = %+v", res)
	}
}

func TestLeagueInvariantViolatedIsFailedResult(t *testing.T) {
	// No realistic fixture breaks the invariant through the file-based
	// wiring (league rules are always scoped), so the report is injected.
	restore := evaluation.SetLeagueRun(func(context.Context, *sql.DB, *config.Config, evalwiring.LeagueOptions, time.Time) (leaguebacktest.Report, error) {
		return leaguebacktest.Report{Articles: 3, InvariantViolations: []string{"a1", "a2"}}, nil
	})
	defer restore()
	f := newFixture(t)
	res, err := f.run("backtest.league", `{}`)
	if err != nil {
		t.Fatalf("a violated invariant must be a result, not an error: %v", err)
	}
	if res.Summary != "FAILED: invariant violated for 2 articles" || !strings.Contains(res.Text, "VIOLATED") {
		t.Fatalf("res = %+v", res)
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
