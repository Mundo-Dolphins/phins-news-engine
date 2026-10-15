package dictionaryfile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
)

func TestLoad_ValidFileDecodesMetaDecisionAndCategories(t *testing.T) {
	d, err := dictionaryfile.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if d.Version != "1.0.0" || d.Name != "test_dictionary" || d.Language != "en" {
		t.Errorf("meta = %+v", d)
	}
	if d.Decision.PositiveThresholdHigh != 10 || d.Decision.MaxCountPerTerm != 3 {
		t.Errorf("Decision = %+v", d.Decision)
	}
	if len(d.Categories) != 2 {
		t.Fatalf("len(Categories) = %d, want 2", len(d.Categories))
	}
}

func TestLoad_DecodesEntryFieldsIncludingAliasesAndActiveSince(t *testing.T) {
	d, err := dictionaryfile.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var ambiguous *dictionary.Category
	for i := range d.Categories {
		if d.Categories[i].Name == "ambiguous_team_terms" {
			ambiguous = &d.Categories[i]
		}
	}
	if ambiguous == nil {
		t.Fatal("ambiguous_team_terms category not found")
	}
	if len(ambiguous.RequiresAnyOf) != 1 || ambiguous.RequiresAnyOf[0] != "official_names" {
		t.Errorf("RequiresAnyOf = %v, want [official_names]", ambiguous.RequiresAnyOf)
	}
	entry := ambiguous.Entries[0]
	if entry.CanonicalName != "isotopes" || len(entry.Aliases) != 2 {
		t.Errorf("entry = %+v", entry)
	}
	if entry.ActiveSince == nil || entry.ActiveSince.Year() != 2020 {
		t.Errorf("ActiveSince = %v, want 2020-01-01", entry.ActiveSince)
	}
	if entry.Type != dictionary.EntryTypeAmbiguous {
		t.Errorf("Type = %q, want %q", entry.Type, dictionary.EntryTypeAmbiguous)
	}
}

func TestLoad_DoesNotValidate_DuplicateNamesParseSuccessfully(t *testing.T) {
	d, err := dictionaryfile.Load(filepath.Join("testdata", "duplicate_name.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v, want Load to only parse (Validate is the caller's job)", err)
	}
	issues := dictionary.Validate(d)
	found := false
	for _, i := range issues {
		if i.Severity == dictionary.SeverityError {
			found = true
		}
	}
	if !found {
		t.Error("want dictionary.Validate to catch the duplicate this fixture deliberately introduces")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := dictionaryfile.Load(filepath.Join("testdata", "does-not-exist.yml"))
	if err == nil {
		t.Fatal("Load() error = nil, want error for missing file")
	}
}

func TestLoad_InvalidDateFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yml")
	writeFile(t, path, `
meta:
  version: "1.0.0"
decision:
  max_count_per_term: 3
  title_weight_multiplier: 1
categories:
  a:
    weight: 1
    entries:
      - canonical_name: "x"
        source: y
        active_since: "not-a-date"
`)
	_, err := dictionaryfile.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error for an unparseable active_since date")
	}
}

func TestLoad_RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unknown.yml")
	writeFile(t, path, `
meta:
  version: "1.0.0"
unknown_top_level_field: true
decision:
  max_count_per_term: 3
  title_weight_multiplier: 1
categories: {}
`)
	_, err := dictionaryfile.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error for an unknown top-level field")
	}
}

func TestLoad_ComposesCategoriesFromIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "dictionary.yml"), `
meta:
  version: "1.0.0"
  name: test_dictionary
decision:
  max_count_per_term: 3
  title_weight_multiplier: 1
includes:
  - dictionaries/people.yml
  - dictionaries/team.yml
`)
	if err := os.Mkdir(filepath.Join(dir, "dictionaries"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "dictionaries", "people.yml"), `
categories:
  people:
    weight: 8
    entries:
      - canonical_name: Jordan Sample
        type: person
        source: manual
`)
	writeFile(t, filepath.Join(dir, "dictionaries", "team.yml"), `
categories:
  team:
    weight: 10
    strong_override: true
    entries:
      - canonical_name: Springfield Isotopes
        type: team
        source: manual
`)

	d, err := dictionaryfile.Load(filepath.Join(dir, "dictionary.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(d.Categories) != 2 || d.Categories[0].Name != "people" || d.Categories[1].Name != "team" {
		t.Fatalf("Categories = %+v, want composed people and team categories", d.Categories)
	}
}

func TestSave_ComposedDictionaryKeepsCategoriesInIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "dictionary.yml")
	writeFile(t, root, `
meta:
  version: "1.0.0"
  name: test_dictionary
decision:
  max_count_per_term: 3
  title_weight_multiplier: 1
includes:
  - dictionaries/people.yml
`)
	if err := os.Mkdir(filepath.Join(dir, "dictionaries"), 0o700); err != nil {
		t.Fatal(err)
	}
	component := filepath.Join(dir, "dictionaries", "people.yml")
	writeFile(t, component, `
categories:
  people:
    weight: 8
    entries:
      - canonical_name: Jordan Sample
        type: person
        source: manual
`)

	d, err := dictionaryfile.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	d.Version = "1.0.1"
	d.Categories[0].Entries[0].CanonicalName = "Jaylen Wickersham"
	if err := dictionaryfile.Save(root, d); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	componentContents, err := os.ReadFile(component)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(componentContents), "Jaylen Wickersham") {
		t.Fatalf("component does not contain updated category: %s", componentContents)
	}
	rootContents, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rootContents), "categories:") || !strings.Contains(string(rootContents), "includes:") {
		t.Fatalf("root should retain only includes, got: %s", rootContents)
	}
}

func TestSave_RoundTripsThroughLoad(t *testing.T) {
	d1, err := dictionaryfile.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "roundtrip.yml")
	if err := dictionaryfile.Save(outPath, d1); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	d2, err := dictionaryfile.Load(outPath)
	if err != nil {
		t.Fatalf("Load(Save(d1)) error = %v", err)
	}
	if !reflect.DeepEqual(d1, d2) {
		t.Errorf("Load(Save(d1)) = %+v, want %+v", d2, d1)
	}
}

func TestSave_RoundTripsSchemaVersion2Fields(t *testing.T) {
	d1, err := dictionaryfile.Load(filepath.Join("testdata", "schema_v2.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "roundtrip_v2.yml")
	if err := dictionaryfile.Save(outPath, d1); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	d2, err := dictionaryfile.Load(outPath)
	if err != nil {
		t.Fatalf("Load(Save(d1)) error = %v", err)
	}
	if !reflect.DeepEqual(d1, d2) {
		t.Errorf("Load(Save(d1)) = %+v, want %+v", d2, d1)
	}

	// Belt-and-suspenders: assert each schema-v2 field explicitly survived
	// the round trip, so a regression that drops a field from the
	// fromDictionary encode-side struct literal fails loudly and
	// specifically rather than only via the DeepEqual diff above.
	if d2.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", d2.SchemaVersion)
	}
	if d2.Checksum != "deadbeef" {
		t.Errorf("Checksum = %q, want %q", d2.Checksum, "deadbeef")
	}
	if len(d2.Sources) != 2 || d2.Sources[0] != "roster" || d2.Sources[1] != "coaches" {
		t.Errorf("Sources = %v, want [roster coaches]", d2.Sources)
	}
	if d2.SnapshotRef != "fetched-20260801-090000.json" {
		t.Errorf("SnapshotRef = %q", d2.SnapshotRef)
	}
	if d2.ChangeCount != 3 {
		t.Errorf("ChangeCount = %d, want 3", d2.ChangeCount)
	}
	if d2.Tool != "example-tools dictionary update" {
		t.Errorf("Tool = %q", d2.Tool)
	}

	entry := d2.Categories[0].Entries[0]
	if entry.RosterStatus != dictionary.RosterStatusActive {
		t.Errorf("RosterStatus = %q, want %q", entry.RosterStatus, dictionary.RosterStatusActive)
	}
	if entry.Role != "Quarterback" || entry.Position != "QB" || entry.Number != "1" {
		t.Errorf("Role/Position/Number = %q/%q/%q, want Quarterback/QB/1", entry.Role, entry.Position, entry.Number)
	}
}

func TestSave_WritesReadableFilePermissions(t *testing.T) {
	d, err := dictionaryfile.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "perm.yml")
	if err := dictionaryfile.Save(outPath, d); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestLoad_DecodesSchemaVersion2Fields(t *testing.T) {
	d, err := dictionaryfile.Load("testdata/schema_v2.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if d.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", d.SchemaVersion)
	}
	if d.Checksum != "deadbeef" {
		t.Errorf("Checksum = %q, want %q", d.Checksum, "deadbeef")
	}
	if len(d.Sources) != 2 || d.Sources[0] != "roster" || d.Sources[1] != "coaches" {
		t.Errorf("Sources = %v, want [roster coaches]", d.Sources)
	}
	if d.SnapshotRef != "fetched-20260801-090000.json" {
		t.Errorf("SnapshotRef = %q", d.SnapshotRef)
	}
	if d.ChangeCount != 3 {
		t.Errorf("ChangeCount = %d, want 3", d.ChangeCount)
	}
	if d.Tool != "example-tools dictionary update" {
		t.Errorf("Tool = %q", d.Tool)
	}

	entry := d.Categories[0].Entries[0]
	if entry.RosterStatus != dictionary.RosterStatusActive {
		t.Errorf("RosterStatus = %q, want %q", entry.RosterStatus, dictionary.RosterStatusActive)
	}
	if entry.Role != "Quarterback" || entry.Position != "QB" || entry.Number != "1" {
		t.Errorf("Role/Position/Number = %q/%q/%q, want Quarterback/QB/1", entry.Role, entry.Position, entry.Number)
	}
}

func TestLoad_V1FileHasZeroValueSchemaFields(t *testing.T) {
	d, err := dictionaryfile.Load("testdata/valid.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.SchemaVersion != 0 {
		t.Errorf("SchemaVersion = %d, want 0 (absent in v1 fixture)", d.SchemaVersion)
	}
	if d.Checksum != "" || d.SnapshotRef != "" || d.Tool != "" || d.ChangeCount != 0 || len(d.Sources) != 0 {
		t.Errorf("expected all schema-v2 fields zero-valued on a v1 fixture, got %+v", d)
	}
}

func TestSave_WritesExactlyOneFileInTargetDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.yml")

	d, err := dictionaryfile.Load("testdata/valid.yml")
	if err != nil {
		t.Fatalf("Load fixture: %v", err)
	}
	if err := dictionaryfile.Save(path, d); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "dict.yml" {
		t.Fatalf("expected exactly one file named dict.yml in %s, got %v", dir, entries)
	}
}

func TestSave_LeavesNoTempFile_WhenTargetDirMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing-subdir", "dict.yml")

	d, err := dictionaryfile.Load("testdata/valid.yml")
	if err != nil {
		t.Fatalf("Load fixture: %v", err)
	}
	if err := dictionaryfile.Save(path, d); err == nil {
		t.Fatal("expected an error writing into a nonexistent directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no leftover files in %s, got %v", dir, entries)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
