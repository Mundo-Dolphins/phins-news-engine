package editorial_test

import (
	"bytes"
	"context"
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
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialmetrics"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t   *testing.T
	db  *sql.DB
	cfg *config.Config
	log *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	cfg, err := config.Load(testsupport.WriteFixture(t))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return fixture{t: t, db: db, cfg: cfg, log: &bytes.Buffer{}}
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range editorial.Actions() {
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
		Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: "user:ana"}
	return a.Run(context.Background(), env, p)
}

func (f fixture) saveArticle(id, title string) {
	f.t.Helper()
	err := sqliteadapter.NewArticleRepository(f.db).Save(context.Background(), article.Article{
		ID: id, Title: title, Summary: title, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source:    source.Source{ID: testsupport.MustID(f.t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS},
		FetchedAt: now, PublishedAt: now, Status: article.StatusFetched,
	})
	if err != nil {
		f.t.Fatalf("save article: %v", err)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMetadata(t *testing.T) {
	type want struct {
		kind        action.Kind
		modes       int
		longRunning bool
		exclusive   bool
	}
	cases := map[string]want{
		"editorial.metrics":        {action.KindRead, 2, false, false},
		"editorial.review":         {action.KindRead, 2, false, false},
		"editorial.canary_status":  {action.KindRead, 2, false, false},
		"editorial.canary_promote": {action.KindRead, 2, false, false},
		"editorial.canary_abort":   {action.KindRead, 2, false, false},
		"editorial.veto_report":    {action.KindRead, 1, false, false},
		"editorial.classify":       {action.KindWrite, 1, true, true},
		"editorial.classify_eval":  {action.KindWrite, 1, true, false},
	}
	as := editorial.Actions()
	if len(as) != len(cases) {
		t.Fatalf("got %d actions, want %d", len(as), len(cases))
	}
	var reg action.Registry
	reg.Add(as...)
	for _, a := range as {
		w, ok := cases[a.Name]
		if !ok {
			t.Fatalf("unexpected action %s", a.Name)
		}
		if a.Group != "editorial" || a.Kind != w.kind || len(a.Modes) != w.modes || a.LongRunning != w.longRunning || a.Exclusive != w.exclusive {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		if w.modes == 1 && a.Modes[0] != action.ModeLocal {
			t.Errorf("%s must be local-only", a.Name)
		}
		if a.Title == "" || a.Description == "" {
			t.Errorf("%s has no title or description", a.Name)
		}
	}
}

func TestMetricsBadSinceIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	for _, since := range []string{"7d", "24"} {
		_, err := f.run("editorial.metrics", `{"since":"`+since+`"}`)
		wantCode(t, err, action.CodeInvalidParams)
		if !strings.HasPrefix(err.Error(), "since: ") {
			t.Fatalf("err = %v, want it to name since", err)
		}
	}
}

func TestMetricsSummary(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("editorial.metrics", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Object.(editorialmetrics.MetricsReport); !ok || res.Render != "object" {
		t.Fatalf("result = %#v", res)
	}
	if res.Summary != "total=0 reject_rate=0.000 ambiguous_rate=0.000" {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestReviewTable(t *testing.T) {
	f := newFixture(t)
	f.saveArticle("a1", "Isotopes sign big extension")
	err := sqliteadapter.NewDecisionRepository(f.db).Append(context.Background(), appout.Event{
		ArticleID: "a1", OccurredAt: now.Add(-time.Hour), EventType: "editorial_shadow", Score: 82.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.run("editorial.review", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "table" || len(res.Rows) != 1 {
		t.Fatalf("result = %#v", res)
	}
	row := res.Rows[0]
	if row["occurred_at"] != now.Add(-time.Hour).Format(time.RFC3339) || row["event_type"] != "editorial_shadow" ||
		row["score"] != "82.5" || row["title"] != "Isotopes sign big extension" {
		t.Fatalf("row = %#v", row)
	}
	if !strings.HasPrefix(res.Summary, "1 decisions in the last 7 days") || !strings.Contains(res.Summary, "editorial_shadow: 1") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestCanaryStatus(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("editorial.canary_status", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Object.(editorialmetrics.CanaryResult); !ok {
		t.Fatalf("object = %#v", res.Object)
	}
	if !strings.HasPrefix(res.Summary, "FAIL") || !strings.Contains(res.Summary, editorialmetrics.CanaryCoverageNote) {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestCanaryPromoteFailingIsConflict(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("editorial.canary_promote", `{}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "criteria failing (") || !strings.HasSuffix(err.Error(), "); re-run with force to override") {
		t.Fatalf("err = %v", err)
	}
}

func TestCanaryPromoteForce(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("editorial.canary_promote", `{"force":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "text" || !strings.Contains(res.Text, "promotion approved (forced=true)") ||
		!strings.Contains(res.Text, editorialmetrics.CanaryCoverageNote) {
		t.Fatalf("text = %q", res.Text)
	}
	if !strings.Contains(f.log.String(), "level=WARN") {
		t.Fatalf("no warning logged:\n%s", f.log)
	}
}

func TestCanaryAbortText(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("editorial.canary_abort", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "set editorial.mode: shadow in the settings file and deploy; run train.rollback if the promoted model itself needs reverting"
	if res.Render != "text" || res.Text != want {
		t.Fatalf("result = %#v", res)
	}
}

func TestVetoReportDefaultsToConfigVetoPath(t *testing.T) {
	f := newFixture(t)
	f.cfg.Editorial.VetoPath = writeFile(t, "veto.yml", `version: "1"
rules:
  - category: gambling
    reason_code: "veto:gambling:promo"
    patterns: ["free bets"]
`)
	dataset := writeFile(t, "d.yml", `examples:
  - article_id: v1
    title: Free bets before kickoff
    source_id: "rss:test"
    label: reject
    label_source: human
  - article_id: v2
    title: Coach press conference
    source_id: "rss:test"
    label: publish
    label_source: human
`)
	res, err := f.run("editorial.veto_report", `{"dataset":"`+dataset+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	rep, ok := res.Object.(editorialmetrics.VetoImpactReport)
	if !ok || rep.Blocked != 1 || rep.Total != 2 {
		t.Fatalf("object = %#v", res.Object)
	}
	if res.Summary != "blocked=1/2 reduction=0.500" {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestClassifyBadSinceIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.BaseURL = "http://127.0.0.1:1"
	f.cfg.OfflineTraining.LLM.Model = "m"
	f.cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	for _, since := range []string{"7x", "-3d", "2026-13-01"} {
		_, err := f.run("editorial.classify", `{"since":"`+since+`"}`)
		wantCode(t, err, action.CodeInvalidParams)
		if !strings.HasPrefix(err.Error(), "since: ") {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestClassifyMissingLLMConfigIsConflict(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.BaseURL = ""
	_, err := f.run("editorial.classify", `{}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "offline_training.llm.base_url") {
		t.Fatalf("err = %v", err)
	}
}

func TestClassifyFailedBatchesWarns(t *testing.T) {
	f := newFixture(t)
	f.saveArticle("t1", "Story t1")
	f.saveArticle("t2", "Story t2")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls++
		content := "not json"
		if calls == 1 {
			content = `{"labels": [{"article_id": "t1", "label": "publish", "confidence": 0.9, "reason": "on topic"}]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
	}))
	defer srv.Close()
	f.cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	f.cfg.OfflineTraining.LLM.BatchSize = 1
	res, err := f.run("editorial.classify", `{"all":true,"llm_base_url":"`+srv.URL+`","llm_model":"m"}`)
	if err != nil {
		t.Fatalf("classify error = %v, want a result", err)
	}
	if !strings.HasPrefix(res.Summary, "WARNING: 1 of 2 batches failed; 1 proposals saved, review queue ") {
		t.Fatalf("summary = %q", res.Summary)
	}
	rep, ok := res.Object.(trainwiring.ClassifyReport)
	if !ok || rep.Result.ProposalsSaved != 1 {
		t.Fatalf("object = %#v", res.Object)
	}
	if !strings.Contains(f.log.String(), "level=WARN") {
		t.Fatalf("log lacks the batch warning:\n%s", f.log)
	}
}

func TestClassifyEvalNoExamplesIsConflict(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	dataset := writeFile(t, "d.yml", "examples:\n  - article_id: x1\n    title: t\n    label: publish\n    label_source: telegramimport\n")
	_, err := f.run("editorial.classify_eval", `{"dataset":"`+dataset+`","fold":"all","llm_base_url":"http://127.0.0.1:1","llm_model":"m"}`)
	wantCode(t, err, action.CodeConflict)
}

func TestClassifyEvalFailedBatchesWarns(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	dataset := writeFile(t, "d.yml", "examples:\n  - article_id: x1\n    title: one\n    label: publish\n  - article_id: x2\n    title: two\n    label: reject\n")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls++
		content := "not json"
		if calls == 1 {
			content = `{"labels": [{"article_id": "x1", "label": "publish", "confidence": 0.9, "reason": "r"}]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
	}))
	defer srv.Close()
	res, err := f.run("editorial.classify_eval", `{"dataset":"`+dataset+`","fold":"all","batch_size":1,"llm_base_url":"`+srv.URL+`","llm_model":"m"}`)
	if err != nil {
		t.Fatalf("classify_eval error = %v, want a result", err)
	}
	if !strings.HasPrefix(res.Summary, "WARNING: 1 of 2 batches failed; ") {
		t.Fatalf("summary = %q", res.Summary)
	}
	if !strings.Contains(f.log.String(), "level=WARN") || !strings.Contains(f.log.String(), "classify-eval batch failed") {
		t.Fatalf("log lacks the batch error:\n%s", f.log)
	}
}

func TestClassifyEvalMissesWriteFailureKeepsReport(t *testing.T) {
	f := newFixture(t)
	f.cfg.OfflineTraining.LLM.TimeoutSeconds = 5
	dataset := writeFile(t, "d.yml", "examples:\n  - article_id: x1\n    title: one\n    label: publish\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant",
				"content": `{"labels": [{"article_id": "x1", "label": "publish", "confidence": 0.9, "reason": "r"}]}`}}},
		})
	}))
	defer srv.Close()
	misses := filepath.Join(t.TempDir(), "missing-dir", "misses.csv")
	res, err := f.run("editorial.classify_eval", `{"dataset":"`+dataset+`","fold":"all","misses_out":"`+misses+
		`","llm_base_url":"`+srv.URL+`","llm_model":"m"}`)
	if err != nil {
		t.Fatalf("classify_eval error = %v, want a result", err)
	}
	if !strings.HasPrefix(res.Summary, "WARNING: could not write misses CSV: ") ||
		!strings.Contains(res.Summary, "; precision=") || !strings.HasSuffix(res.Summary, "on 1 examples") {
		t.Fatalf("summary = %q", res.Summary)
	}
	rep, ok := res.Object.(trainwiring.ClassifyEvalReport)
	if !ok || rep.Examples != 1 || rep.Report.BatchesTotal != 1 || rep.MissesWritten != "" {
		t.Fatalf("object = %#v", res.Object)
	}
	if !strings.Contains(f.log.String(), "level=WARN") || !strings.Contains(f.log.String(), "misses CSV") {
		t.Fatalf("log lacks the misses warning:\n%s", f.log)
	}
}
