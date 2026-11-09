package dictionary_test

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

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/dictionary"
	domaindict "github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

var decision = domaindict.Decision{PositiveThresholdHigh: 3, PositiveThresholdMedium: 1, TitleWeightMultiplier: 1, MaxCountPerTerm: 3}

func dict(version string, names ...string) domaindict.Dictionary {
	entries := make([]domaindict.Entry, 0, len(names))
	for _, n := range names {
		entries = append(entries, domaindict.Entry{CanonicalName: n, Source: "roster", Confidence: 0.5})
	}
	return domaindict.Dictionary{
		Version: version, Name: "test_dictionary", Language: "en", Decision: decision,
		Categories: []domaindict.Category{{Name: "active_roster_core", Weight: 1, Entries: entries}},
	}
}

// strongDict has the strong-override team name the regression dataset needs.
func strongDict(version string, withTeam bool) domaindict.Dictionary {
	var entries []domaindict.Entry
	if withTeam {
		entries = append(entries, domaindict.Entry{CanonicalName: "Springfield Isotopes", Source: "manual", Confidence: 1})
	}
	return domaindict.Dictionary{
		Version: version, Name: "test_dictionary", Language: "en",
		Decision:   domaindict.Decision{PositiveThresholdHigh: 1, PositiveThresholdMedium: 0.5, MaxCountPerTerm: 3, TitleWeightMultiplier: 2},
		Categories: []domaindict.Category{{Name: "official_names", Weight: 5, StrongOverride: true, Entries: entries}},
	}
}

type fixture struct {
	t   *testing.T
	db  *sql.DB
	cfg *config.Config
	log *bytes.Buffer
	dir string
}

// newFixture chdirs into a temp dir (history artifacts and snapshots live
// under relative default directories), records history versions 1.0.0 and
// 1.0.1 with their artifacts and writes the current dictionary at 1.0.1.
func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	db, err := infrasqlite.Open(filepath.Join(dir, "t.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	for i, d := range []domaindict.Dictionary{dict("1.0.0", "Springfield Isotopes"), dict("1.0.1", "Springfield Isotopes", "Some New Entity")} {
		if err := dictionaryhistory.WriteVersion(dictionaryhistory.DefaultDir, dictionaryhistory.Artifacts{Version: d.Version, Dictionary: d, Diff: "diff of " + d.Version}); err != nil {
			t.Fatal(err)
		}
		if err := repo.Record(context.Background(), out.DictionaryHistoryRecord{
			Version: d.Version, ContentHash: domaindict.ContentHash(d), CreatedAt: now.Add(time.Duration(i) * time.Hour), OperationKind: "update",
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Dictionary.Path = filepath.Join(dir, "dictionary.yml")
	if err := dictionaryfile.Save(cfg.Dictionary.Path, dict("1.0.1", "Springfield Isotopes", "Some New Entity")); err != nil {
		t.Fatal(err)
	}
	return fixture{t: t, db: db, cfg: cfg, log: &bytes.Buffer{}, dir: dir}
}

func (f fixture) runIn(mode action.Mode, name, params string) (action.Result, error) {
	f.t.Helper()
	for _, a := range dictionary.Actions() {
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

func (f fixture) historyCount() int {
	f.t.Helper()
	list, err := sqliteadapter.NewDictionaryHistoryRepository(f.db).List(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return len(list)
}

func (f fixture) save(name string, d domaindict.Dictionary) string {
	f.t.Helper()
	p := filepath.Join(f.dir, name)
	if err := dictionaryfile.Save(p, d); err != nil {
		f.t.Fatal(err)
	}
	return p
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
		"dictionary.validate":         {action.KindRead, 2, false, false},
		"dictionary.inspect":          {action.KindRead, 2, false, false},
		"dictionary.history":          {action.KindRead, 2, false, false},
		"dictionary.diff":             {action.KindRead, 2, false, false},
		"dictionary.regression_check": {action.KindRead, 1, true, true},
		"dictionary.rollback":         {action.KindDestructive, 1, false, false},
		"dictionary.mine_candidates":  {action.KindRead, 1, false, false},
	}
	as := dictionary.Actions()
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
		if a.Group != "dictionary" || a.Kind != w.kind || len(a.Modes) != w.modes || a.LongRunning != w.longRunning || a.Exclusive != w.exclusive {
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

const duplicateYAML = `
meta:
  version: "1.0.0"
  name: dup
  language: en
decision:
  positive_threshold_high: 10
  positive_threshold_medium: 6
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  a:
    weight: 1
    entries:
      - canonical_name: "isotopes"
        source: x
  b:
    weight: 1
    entries:
      - canonical_name: "Isotopes"
        source: y
`

func TestValidateOK(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("dictionary.validate", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "dictionary OK: " + f.cfg.Dictionary.Path + " (0 issue(s) reported)"; res.Summary != want {
		t.Errorf("Summary = %q, want %q", res.Summary, want)
	}
}

func TestValidateErrorIssues(t *testing.T) {
	f := newFixture(t)
	bad := filepath.Join(f.dir, "dup.yml")
	if err := os.WriteFile(bad, []byte(duplicateYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := f.run("dictionary.validate", `{"path":"`+bad+`"}`)
	if err != nil {
		t.Fatalf("error issues must be a result, got error %v", err)
	}
	if want := "FAILED: " + bad + " failed validation"; res.Summary != want {
		t.Errorf("Summary = %q, want %q", res.Summary, want)
	}
	if len(res.Rows) == 0 || res.Rows[0]["severity"] != "error" || res.Rows[0]["message"] == "" || res.Rows[0]["location"] == "" {
		t.Errorf("rows = %+v", res.Rows)
	}
}

func TestValidateMissingFileIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("dictionary.validate", `{"path":"`+filepath.Join(f.dir, "missing.yml")+`"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestValidateAndInspectRefusePathInProduction(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"dictionary.validate", "dictionary.inspect"} {
		_, err := f.runIn(action.ModeProduction, name, `{"path":"/etc/passwd"}`)
		wantCode(t, err, action.CodeInvalidParams)
		if !strings.Contains(err.Error(), "path") {
			t.Errorf("%s error %q does not name the param", name, err)
		}
		if _, err := f.runIn(action.ModeProduction, name, `{"path":"`+f.cfg.Dictionary.Path+`"}`); err != nil {
			t.Errorf("%s with the configured path: %v", name, err)
		}
	}
}

func TestInspect(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("dictionary.inspect", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Object)
	var got struct {
		Version      string `json:"version"`
		TotalEntries int    `json:"total_entries"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.Version != "1.0.1" || got.TotalEntries != 2 {
		t.Errorf("object = %s", b)
	}
}

func TestHistory(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("dictionary.history", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "table" || len(res.Rows) != 2 || res.Rows[0]["version"] != "1.0.1" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	res, err = f.run("dictionary.history", `{"version":"1.0.0"}`)
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := res.Object.(map[string]any)
	if res.Render != "object" || obj["version"] != "1.0.0" || obj["diff"] != "diff of 1.0.0" {
		t.Errorf("object = %+v", res.Object)
	}
}

func TestHistoryUnknownVersionIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("dictionary.history", `{"version":"9.9.9"}`)
	wantCode(t, err, action.CodeNotFound)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want it to wrap sql.ErrNoRows", err)
	}
}

func TestDiffRefusesFromFileInProduction(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ params, param string }{
		{`{"from_file":"/etc/a.yml","to_file":"/etc/b.yml"}`, "from_file"},
		{`{"to_file":"/etc/b.yml"}`, "to_file"},
		{`{"snapshot":"/etc/s.json"}`, "snapshot"},
		{`{"snapshot_dir":"/etc"}`, "snapshot_dir"},
		{`{"dictionary":"/etc/d.yml"}`, "dictionary"},
	} {
		_, err := f.runIn(action.ModeProduction, "dictionary.diff", tc.params)
		wantCode(t, err, action.CodeInvalidParams)
		if !strings.Contains(err.Error(), tc.param) {
			t.Errorf("%s: error %q does not name the param", tc.params, err)
		}
	}
	// History versions and the configured values are allowed.
	res, err := f.runIn(action.ModeProduction, "dictionary.diff", `{"from":"1.0.0","to":"1.0.1","dictionary":"`+f.cfg.Dictionary.Path+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0]["kind"] != "add_entity" || res.Rows[0]["term"] == "" {
		t.Errorf("rows = %+v", res.Rows)
	}
	if !strings.Contains(res.Summary, "add_entity: 1") {
		t.Errorf("Summary = %q", res.Summary)
	}
}

func TestDiffArgumentErrors(t *testing.T) {
	f := newFixture(t)
	for _, params := range []string{`{"to":"1.0.0"}`, `{"from_file":"a.yml"}`} {
		_, err := f.run("dictionary.diff", params)
		wantCode(t, err, action.CodeInvalidParams)
	}
}

func TestDiffNoSnapshotsIsConflict(t *testing.T) {
	f := newFixture(t)
	empty := t.TempDir()
	_, err := f.run("dictionary.diff", `{"snapshot_dir":"`+empty+`"}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), empty) {
		t.Errorf("error %q does not name the snapshot dir", err)
	}
	// The configured default dir in production, empty too.
	if err := os.MkdirAll("data/dictionary/snapshots", 0o750); err != nil {
		t.Fatal(err)
	}
	_, err = f.runIn(action.ModeProduction, "dictionary.diff", `{}`)
	wantCode(t, err, action.CodeConflict)
}

func TestDiffUnknownVersionIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("dictionary.diff", `{"from":"9.9.9"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestRollbackNeedsExactlyOneTarget(t *testing.T) {
	f := newFixture(t)
	for _, params := range []string{`{}`, `{"to":"1.0.0","previous":true}`} {
		_, err := f.run("dictionary.rollback", params)
		wantCode(t, err, action.CodeInvalidParams)
	}
}

func TestRollbackChecksumIsConflict(t *testing.T) {
	f := newFixture(t)
	if err := dictionaryhistory.WriteVersion(dictionaryhistory.DefaultDir, dictionaryhistory.Artifacts{
		Version: "1.0.0", Dictionary: dict("1.0.0", "Tampered Entity"),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.cfg.Dictionary.Path)
	if err != nil {
		t.Fatal(err)
	}
	rows := f.historyCount()
	for _, params := range []string{`{"to":"1.0.0"}`, `{"to":"1.0.0","dry_run":true}`} {
		_, err := f.run("dictionary.rollback", params)
		wantCode(t, err, action.CodeConflict)
	}
	after, _ := os.ReadFile(f.cfg.Dictionary.Path)
	if !bytes.Equal(before, after) {
		t.Error("dictionary file changed after a refused rollback")
	}
	if f.historyCount() != rows {
		t.Error("history rows changed after a refused rollback")
	}
}

func TestRollbackNoPreviousVersionIsConflict(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(`DELETE FROM dictionary_history WHERE version = '1.0.0'`); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("dictionary.rollback", `{"previous":true}`)
	wantCode(t, err, action.CodeConflict)
}

func TestRollbackUnknownVersionIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("dictionary.rollback", `{"to":"9.9.9"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestRollbackDryRun(t *testing.T) {
	f := newFixture(t)
	before, _ := os.ReadFile(f.cfg.Dictionary.Path)
	rows := f.historyCount()
	res, err := f.run("dictionary.rollback", `{"to":"1.0.0","dry_run":true}`)
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := res.Object.(map[string]any)
	if obj["dry_run"] != true || obj["written"] != "" || obj["from"] != "1.0.1" || obj["restored_from"] != "1.0.0" {
		t.Errorf("object = %+v", res.Object)
	}
	if !strings.Contains(res.Text, "Possible departures (1)") {
		t.Errorf("Text = %q", res.Text)
	}
	if res.Summary != "dry-run: no changes written" {
		t.Errorf("Summary = %q", res.Summary)
	}
	after, _ := os.ReadFile(f.cfg.Dictionary.Path)
	if !bytes.Equal(before, after) || f.historyCount() != rows {
		t.Error("a dry run changed the dictionary or the history")
	}
}

func TestRollbackWrites(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("dictionary.rollback", `{"previous":true}`)
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := res.Object.(map[string]any)
	written, _ := obj["written"].(string)
	if written == "" || obj["restored_from"] != "1.0.0" || obj["dry_run"] != false {
		t.Fatalf("object = %+v", res.Object)
	}
	d, err := dictionaryfile.Load(f.cfg.Dictionary.Path)
	if err != nil || d.Version != written {
		t.Errorf("dictionary version = %q (%v), want %q", d.Version, err, written)
	}
	if f.historyCount() != 3 {
		t.Errorf("history rows = %d, want 3", f.historyCount())
	}
}

func TestRegressionCheckOverThreshold(t *testing.T) {
	f := newFixture(t)
	prev := f.save("prev.yml", strongDict("1.0.0", true))
	cand := f.save("cand.yml", strongDict("1.0.1", false))

	res, err := f.run("dictionary.regression_check", `{"previous":"`+prev+`","candidate":"`+cand+`"}`)
	if err != nil {
		t.Fatalf("over threshold must be a result, got error %v", err)
	}
	if !strings.HasPrefix(res.Summary, "FAILED: ") || !strings.Contains(res.Summary, "cases flipped") || len(res.Rows) == 0 {
		t.Errorf("Summary = %q, rows = %d", res.Summary, len(res.Rows))
	}
	res, err = f.run("dictionary.regression_check", `{"previous":"`+prev+`","candidate":"`+prev+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(res.Summary, "FAILED") || !strings.HasSuffix(res.Summary, " cases flipped") || !strings.HasPrefix(res.Summary, "0/") {
		t.Errorf("Summary = %q", res.Summary)
	}
	_, err = f.run("dictionary.regression_check", `{"candidate":"`+prev+`","flip_threshold":-1}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestMineCandidatesNoSessionIsConflict(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("dictionary.mine_candidates", `{}`)
	wantCode(t, err, action.CodeConflict)
}
