package classifierdictionary_test

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

	sqliteclassifierproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteclassifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/classifierdictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
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
  nfl_people_context:
    weight: 1
    entries: []
`

type fixture struct {
	t    *testing.T
	db   *sql.DB
	cfg  *config.Config
	dict string
	log  *bytes.Buffer
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
	dict := filepath.Join(t.TempDir(), "dictionary.yml")
	if err := os.WriteFile(dict, []byte(dictionaryYAML), 0o600); err != nil {
		t.Fatalf("write dictionary: %v", err)
	}
	cfg.Dictionary.Path = dict
	return fixture{t: t, db: db, cfg: cfg, dict: dict, log: &bytes.Buffer{}}
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range classifierdictionary.Actions() {
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

func (f fixture) seed(value string, occurrences int, status classifierproposal.Status) string {
	f.t.Helper()
	id, err := sqliteclassifierproposal.NewClassifierProposalRepository(f.db).Upsert(context.Background(), classifierproposal.Proposal{
		Kind: classifierproposal.KindTerm, Category: "nfl_people_context", Value: value,
		NormalizedValue: classifierproposal.Normalize(value), Operation: "add",
		Metadata: map[string]string{"origin": "test"}, Confidence: 0.8, Occurrences: occurrences,
		Sources: []classifierproposal.Source{{ArticleID: "a1", ArticleTitle: "Title one"}},
		Status:  classifierproposal.StatusPending, FirstSeen: now, LastSeen: now, AnalyzerVersion: "v1", CreatedAt: now,
	})
	if err != nil {
		f.t.Fatalf("Upsert: %v", err)
	}
	if status != classifierproposal.StatusPending {
		if err := sqliteclassifierproposal.NewClassifierProposalRepository(f.db).UpdateStatus(context.Background(), id, status, now); err != nil {
			f.t.Fatalf("UpdateStatus: %v", err)
		}
	}
	return id
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
		"classifier_dictionary.analyze": {action.KindWrite, 1, true, true},
		"classifier_dictionary.list":    {action.KindRead, 2, false, false},
		"classifier_dictionary.show":    {action.KindRead, 2, false, false},
		"classifier_dictionary.approve": {action.KindWrite, 1, false, false},
		"classifier_dictionary.reject":  {action.KindWrite, 1, false, false},
		"classifier_dictionary.metrics": {action.KindRead, 2, false, false},
	}
	as := classifierdictionary.Actions()
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
		if a.Group != "classifier_dictionary" || a.Kind != w.kind || len(a.Modes) != w.modes || a.LongRunning != w.longRunning || a.Exclusive != w.exclusive {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		if w.modes == 1 && a.Modes[0] != action.ModeLocal {
			t.Errorf("%s must be local-only", a.Name)
		}
		if a.Title == "" || a.Description == "" {
			t.Errorf("%s has no title or description", a.Name)
		}
		props, _ := action.Schema(a.Params)["properties"].(map[string]any)
		for name, p := range props {
			if prop, _ := p.(map[string]any); prop["description"] == nil {
				t.Errorf("%s param %s has no help", a.Name, name)
			}
		}
	}
}

func TestListDefaultsToPendingSortedByScore(t *testing.T) {
	f := newFixture(t)
	low := f.seed("Quill Farrowby", 2, classifierproposal.StatusPending)
	high := f.seed("Zeno Ashbury", 9, classifierproposal.StatusPending)
	f.seed("Orrin Pellwick", 20, classifierproposal.StatusRejected)
	res, err := f.run("classifier_dictionary.list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || res.Rows[0]["id"] != high || res.Rows[1]["id"] != low {
		t.Fatalf("rows = %+v, want %s then %s", res.Rows, high, low)
	}
	if res.Rows[0]["confidence"] != "0.80" || res.Rows[0]["value"] != "Zeno Ashbury" || res.Rows[0]["status"] != "pending" {
		t.Fatalf("row = %+v", res.Rows[0])
	}
}

func TestListStatusAll(t *testing.T) {
	f := newFixture(t)
	f.seed("Quill Farrowby", 2, classifierproposal.StatusPending)
	f.seed("Orrin Pellwick", 20, classifierproposal.StatusRejected)
	f.seed("Zeno Ashbury", 9, classifierproposal.StatusApproved)
	res, err := f.run("classifier_dictionary.list", `{"status":"all"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(res.Rows))
	}
}

func TestShow(t *testing.T) {
	f := newFixture(t)
	id := f.seed("Zeno Ashbury", 9, classifierproposal.StatusPending)
	res, err := f.run("classifier_dictionary.show", `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Object)
	for _, w := range []string{`"value":"Zeno Ashbury"`, `"origin":"test"`, `"article_title":"Title one"`} {
		if !strings.Contains(string(b), w) {
			t.Errorf("object %s lacks %s", b, w)
		}
	}
}

func TestShowUnknownIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("classifier_dictionary.show", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestApproveWritesDictionary(t *testing.T) {
	f := newFixture(t)
	id := f.seed("Zeno Ashbury", 9, classifierproposal.StatusPending)
	res, err := f.run("classifier_dictionary.approve", `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "approved Zeno Ashbury ("+id+")" {
		t.Fatalf("text = %q", res.Text)
	}
	body, _ := os.ReadFile(f.dict)
	if !strings.Contains(string(body), "Zeno Ashbury") {
		t.Fatalf("dictionary lacks the term:\n%s", body)
	}
}

func TestApproveNotPendingIsConflictAndDictionaryUntouched(t *testing.T) {
	f := newFixture(t)
	id := f.seed("Zeno Ashbury", 9, classifierproposal.StatusRejected)
	before, _ := os.ReadFile(f.dict)
	_, err := f.run("classifier_dictionary.approve", `{"id":"`+id+`"}`)
	wantCode(t, err, action.CodeConflict)
	after, _ := os.ReadFile(f.dict)
	if !bytes.Equal(before, after) {
		t.Fatal("dictionary changed")
	}
	p, _ := sqliteclassifierproposal.NewClassifierProposalRepository(f.db).Get(context.Background(), id)
	if p.Status != classifierproposal.StatusRejected {
		t.Fatalf("status = %s", p.Status)
	}
}

func TestApproveUnknownIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("classifier_dictionary.approve", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestReject(t *testing.T) {
	f := newFixture(t)
	id := f.seed("Zeno Ashbury", 9, classifierproposal.StatusPending)
	res, err := f.run("classifier_dictionary.reject", `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "rejected Zeno Ashbury ("+id+")" {
		t.Fatalf("text = %q", res.Text)
	}
	_, err = f.run("classifier_dictionary.reject", `{"id":"`+id+`"}`)
	wantCode(t, err, action.CodeConflict)
	_, err = f.run("classifier_dictionary.reject", `{"id":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestMetrics(t *testing.T) {
	f := newFixture(t)
	f.seed("Zeno Ashbury", 9, classifierproposal.StatusPending)
	f.seed("Orrin Pellwick", 2, classifierproposal.StatusRejected)
	res, err := f.run("classifier_dictionary.metrics", `{"since":"87600h"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "2 proposals, 0 approved, 1 rejected" {
		t.Fatalf("summary = %q", res.Summary)
	}
	if _, err := f.run("classifier_dictionary.metrics", `{}`); err != nil {
		t.Fatalf("default since: %v", err)
	}
}

func TestMetricsBadSinceIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("classifier_dictionary.metrics", `{"since":"7d"}`)
	wantCode(t, err, action.CodeInvalidParams)
	if !strings.HasPrefix(err.Error(), "since: ") {
		t.Fatalf("err = %v, want it to name since", err)
	}
}

func TestAnalyzeBadSinceIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	for _, since := range []string{"7x", "-3d", "2026-13-01"} {
		_, err := f.run("classifier_dictionary.analyze", `{"since":"`+since+`"}`)
		wantCode(t, err, action.CodeInvalidParams)
		if !strings.HasPrefix(err.Error(), "since: ") {
			t.Fatalf("err = %v, want it to name since", err)
		}
	}
}

func TestAnalyzeEmptyDatabase(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("classifier_dictionary.analyze", `{"since":"7d"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "0 articles, 0 article proposals, 0 unknown-entity proposals" {
		t.Fatalf("summary = %q", res.Summary)
	}
}
