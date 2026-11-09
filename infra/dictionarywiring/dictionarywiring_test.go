package dictionarywiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysnapshot"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

const validYAML = `
meta:
  version: "1.0.0"
  name: test_dict
  language: en
decision:
  positive_threshold_high: 10
  positive_threshold_medium: 6
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  official_names:
    weight: 12
    strong_override: true
    entries:
      - canonical_name: "Springfield Isotopes"
        aliases: ["Isotopes", "Topes"]
        source: manual
        confidence: 1.0
  rivals:
    weight: -3
    entries:
      - canonical_name: "Shelbyville Shakes"
        source: manual
        confidence: 1.0
      - canonical_name: "Capital City Goofballs"
        aliases: ["Goofballs"]
        source: manual
        confidence: 1.0
`

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

func write(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dictionary.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateReportsErrorIssues(t *testing.T) {
	_, issues, err := Validate(write(t, duplicateYAML))
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !HasErrors(issues) {
		t.Fatalf("HasErrors(%v) = false, want true", issues)
	}
	_, issues, err = Validate(write(t, validYAML))
	if err != nil || HasErrors(issues) {
		t.Fatalf("valid dictionary: err = %v, issues = %v", err, issues)
	}
	if _, _, err := Validate(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatal("Validate() on a missing file: error = nil")
	}
}

func TestInspectCounts(t *testing.T) {
	r, err := Inspect(write(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.0.0" || r.Name != "test_dict" || r.Language != "en" {
		t.Errorf("header = %+v", r)
	}
	if r.TotalEntries != 3 || r.TotalAliases != 3 || len(r.Categories) != 2 {
		t.Fatalf("totals = %d entries, %d aliases, %d categories", r.TotalEntries, r.TotalAliases, len(r.Categories))
	}
	got := r.Categories[1]
	if got.Name != "rivals" || got.Entries != 2 || got.Aliases != 1 || got.Weight != -3 || got.StrongOverride {
		t.Errorf("rivals = %+v", got)
	}
	if r.Decision.MaxCountPerTerm != 3 {
		t.Errorf("decision = %+v", r.Decision)
	}
}

func TestDiffArgumentErrors(t *testing.T) {
	if _, err := Diff(DiffOptions{To: "1.0.0"}, time.Now()); err == nil || err.Error() != "--to requires --from" {
		t.Errorf("To without From: error = %v", err)
	}
	if _, err := Diff(DiffOptions{FromFile: "a.yml"}, time.Now()); err == nil || err.Error() != "--from-file and --to-file must be given together" {
		t.Errorf("only FromFile: error = %v", err)
	}
}

func TestDiffNoSnapshots(t *testing.T) {
	dir := t.TempDir()
	_, err := Diff(DiffOptions{DictionaryPath: write(t, validYAML), SnapshotDir: dir}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "no snapshot files found in "+dir) {
		t.Fatalf("error = %v", err)
	}
}

func TestDiffFiles(t *testing.T) {
	props, err := Diff(DiffOptions{FromFile: write(t, validYAML), ToFile: write(t, validYAML)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = props
}

func TestDefaultSnapshotDir(t *testing.T) {
	if dictionarysnapshot.DefaultDir != "data/dictionary/snapshots" {
		t.Fatalf("DefaultDir = %q", dictionarysnapshot.DefaultDir)
	}
}

func TestHistoryVersionNotFound(t *testing.T) {
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "t.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, _, err := HistoryVersion(context.Background(), db, "9.9.9"); err == nil {
		t.Fatal("HistoryVersion() error = nil, want not found")
	}
	list, err := History(context.Background(), db)
	if err != nil || len(list) != 0 {
		t.Fatalf("History() = %v, %v", list, err)
	}
}
