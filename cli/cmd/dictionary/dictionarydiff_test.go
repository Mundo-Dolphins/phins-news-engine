package dictionary_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysnapshot"
)

const diffFixtureDictionaryYAML = `
meta:
  version: "1.0.0"
  name: isotopes
  language: en
  generated_at: "2026-08-01"
decision:
  positive_threshold_high: 5
  positive_threshold_medium: 2
  negative_threshold_high: -3
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  active_roster_core:
    weight: 1
    entries:
      - canonical_name: "Existing Player"
        type: person
        position: "WR"
        number: "10"
        confidence: 0.9
`

func writeDiffFixtureDictionary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.yml")
	if err := os.WriteFile(path, []byte(diffFixtureDictionaryYAML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func writeDiffFixtureSnapshot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	entities := []dictionaryupdate.SourceEntity{
		{DisplayName: "Existing Player", CanonicalKey: "existing player", Kind: dictionaryupdate.SourceKindRoster, Position: "WR", Number: "10"},
		{DisplayName: "New Player", CanonicalKey: "new player", Kind: dictionaryupdate.SourceKindRoster, Position: "RB", Number: "22"},
	}
	snap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200, entities, nil, time.Now())
	path, err := dictionarysnapshot.Write(dir, snap, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return path
}

// writeDiffFixtureSnapshotDir writes two snapshot files for two different
// source kinds (roster, coaches) into a fresh directory, proving that the
// default (no --snapshot) mode must merge the latest snapshot *per source*
// rather than picking a single file. the host's dictionary fetcher writes one file per
// source per run (fetched-<source>-<timestamp>.json), so a directory
// normally holds several current files at once.
func writeDiffFixtureSnapshotDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	rosterEntities := []dictionaryupdate.SourceEntity{
		{DisplayName: "Existing Player", CanonicalKey: "existing player", Kind: dictionaryupdate.SourceKindRoster, Position: "WR", Number: "10"},
		{DisplayName: "New Roster Player", CanonicalKey: "new roster player", Kind: dictionaryupdate.SourceKindRoster, Position: "RB", Number: "22"},
	}
	rosterSnap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200, rosterEntities, nil, time.Now())
	if _, err := dictionarysnapshot.Write(dir, rosterSnap, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("write roster snapshot: %v", err)
	}

	coachEntities := []dictionaryupdate.SourceEntity{
		{DisplayName: "New Coach", CanonicalKey: "new coach", Kind: dictionaryupdate.SourceKindCoaches, Role: "Offensive Coordinator"},
	}
	coachSnap := dictionarysnapshot.New(dictionaryupdate.SourceKindCoaches, "https://y", "y", "auto", 200, coachEntities, nil, time.Now())
	if _, err := dictionarysnapshot.Write(dir, coachSnap, time.Date(2026, 8, 1, 9, 0, 5, 0, time.UTC)); err != nil {
		t.Fatalf("write coaches snapshot: %v", err)
	}

	return dir
}

func TestDictionaryDiff_TextFormat_ListsNewEntityAndUnchangedCount(t *testing.T) {
	dictPath := writeDiffFixtureDictionary(t)
	snapPath := writeDiffFixtureSnapshot(t)

	out, err := testsupport.Execute(t, "dictionary", "diff", "--dictionary", dictPath, "--snapshot", snapPath, "--format", "text")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "New entities (1):") {
		t.Errorf("expected a 'New entities (1):' section, got:\n%s", out)
	}
	if !strings.Contains(out, "new player") {
		t.Errorf("expected the new entity's canonical key in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Unchanged: 1") {
		t.Errorf("expected 'Unchanged: 1', got:\n%s", out)
	}
}

func TestDictionaryDiff_JSONFormat_IsValidJSON(t *testing.T) {
	dictPath := writeDiffFixtureDictionary(t)
	snapPath := writeDiffFixtureSnapshot(t)

	out, err := testsupport.Execute(t, "dictionary", "diff", "--dictionary", dictPath, "--snapshot", snapPath, "--format", "json")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v", err)
	}

	var proposals []dictionaryupdate.Proposal
	if err := json.Unmarshal([]byte(out), &proposals); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	if len(proposals) != 2 {
		t.Fatalf("got %d proposals, want 2 (1 add_entity + 1 no_change)", len(proposals))
	}
}

func TestDictionaryDiff_FromFileToFile(t *testing.T) {
	fromPath := writeDiffFixtureDictionary(t)
	toPath := writeDiffFixtureDictionary(t) // identical content -> everything unchanged

	out, err := testsupport.Execute(t, "dictionary", "diff", "--from-file", fromPath, "--to-file", toPath, "--format", "text")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "Unchanged: 1") {
		t.Errorf("expected 'Unchanged: 1' when diffing identical files, got:\n%s", out)
	}
}

func TestDictionaryDiff_DefaultMode_MergesLatestSnapshotPerSource(t *testing.T) {
	dictPath := writeDiffFixtureDictionary(t)
	dir := writeDiffFixtureSnapshotDir(t)

	out, err := testsupport.Execute(t, "dictionary", "diff", "--dictionary", dictPath, "--snapshot-dir", dir, "--format", "json")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}

	var proposals []dictionaryupdate.Proposal
	if err := json.Unmarshal([]byte(out), &proposals); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}

	addEntityKeys := make(map[string]bool)
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalAddEntity {
			addEntityKeys[p.CanonicalKey] = true
		}
	}
	if !addEntityKeys["new roster player"] {
		t.Errorf("expected 'new roster player' (from the roster snapshot) to be proposed as add_entity, got proposals: %+v", proposals)
	}
	if !addEntityKeys["new coach"] {
		t.Errorf("expected 'new coach' (from the DIFFERENT coaches snapshot) to be proposed as add_entity -- default mode must merge across sources, not just read one file; got proposals: %+v", proposals)
	}
}

// diffHistoryDict builds a roster-only dictionary with the given version and
// canonical names, valid enough to round-trip through the history artifacts.
func diffHistoryDict(version string, names ...string) dictionary.Dictionary {
	entries := make([]dictionary.Entry, 0, len(names))
	for _, n := range names {
		entries = append(entries, dictionary.Entry{CanonicalName: n, Source: "roster", Confidence: 0.5})
	}
	return dictionary.Dictionary{
		Version: version, Name: "isotopes", Language: "en",
		Decision: dictionary.Decision{
			PositiveThresholdHigh: 5, PositiveThresholdMedium: 2, NegativeThresholdHigh: -3,
			TitleWeightMultiplier: 2, MaxCountPerTerm: 3,
		},
		Categories: []dictionary.Category{{Name: "active_roster_core", Weight: 1, Entries: entries}},
	}
}

func TestDictionaryDiff_FromToHistoricalVersions(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	historyDir := filepath.Join(dir, "data", "dictionary", "history")
	if err := dictionaryhistory.WriteVersion(historyDir, dictionaryhistory.Artifacts{
		Version: "1.0.0", Dictionary: diffHistoryDict("1.0.0", "Existing Player"),
	}); err != nil {
		t.Fatalf("write history 1.0.0: %v", err)
	}
	if err := dictionaryhistory.WriteVersion(historyDir, dictionaryhistory.Artifacts{
		Version: "1.0.1", Dictionary: diffHistoryDict("1.0.1", "Existing Player", "New Player"),
	}); err != nil {
		t.Fatalf("write history 1.0.1: %v", err)
	}

	out, err := testsupport.Execute(t, "dictionary", "diff", "--from", "1.0.0", "--to", "1.0.1", "--format", "json")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	var proposals []dictionaryupdate.Proposal
	if err := json.Unmarshal([]byte(out), &proposals); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	found := false
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalAddEntity && p.CanonicalKey == "new player" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'new player' proposed as add_entity when diffing 1.0.0 -> 1.0.1, got: %+v", proposals)
	}
}

func TestDictionaryDiff_FromHistoricalToCurrentOnDisk(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	historyDir := filepath.Join(dir, "data", "dictionary", "history")
	if err := dictionaryhistory.WriteVersion(historyDir, dictionaryhistory.Artifacts{
		Version: "1.0.0", Dictionary: diffHistoryDict("1.0.0", "Existing Player"),
	}); err != nil {
		t.Fatalf("write history 1.0.0: %v", err)
	}
	currentPath := filepath.Join(dir, "current.yml")
	if err := dictionaryfile.Save(currentPath, diffHistoryDict("1.0.5", "Existing Player", "Current Extra")); err != nil {
		t.Fatalf("save current: %v", err)
	}

	out, err := testsupport.Execute(t, "dictionary", "diff", "--from", "1.0.0", "--dictionary", currentPath, "--format", "json")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	var proposals []dictionaryupdate.Proposal
	if err := json.Unmarshal([]byte(out), &proposals); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	found := false
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalAddEntity && p.CanonicalKey == "current extra" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'current extra' proposed as add_entity when diffing 1.0.0 -> current on disk, got: %+v", proposals)
	}
}

func TestDictionaryDiff_ToWithoutFromFails(t *testing.T) {
	out, err := testsupport.Execute(t, "dictionary", "diff", "--to", "1.0.1")
	if err == nil {
		t.Fatalf("expected an error when --to is given without --from, got none\noutput:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--to requires --from") {
		t.Errorf("expected a '--to requires --from' error, got: %v", err)
	}
}

func TestDictionaryDiff_DefaultMode_PicksLatestSnapshotWithinASource(t *testing.T) {
	dictPath := writeDiffFixtureDictionary(t)
	dir := t.TempDir()

	olderEntities := []dictionaryupdate.SourceEntity{
		{DisplayName: "Existing Player", CanonicalKey: "existing player", Kind: dictionaryupdate.SourceKindRoster, Position: "WR", Number: "10"},
		{DisplayName: "Stale Player", CanonicalKey: "stale player", Kind: dictionaryupdate.SourceKindRoster, Position: "RB", Number: "5"},
	}
	olderSnap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200, olderEntities, nil, time.Now())
	if _, err := dictionarysnapshot.Write(dir, olderSnap, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("write older snapshot: %v", err)
	}

	newerEntities := []dictionaryupdate.SourceEntity{
		{DisplayName: "Existing Player", CanonicalKey: "existing player", Kind: dictionaryupdate.SourceKindRoster, Position: "WR", Number: "10"},
		{DisplayName: "Fresh Player", CanonicalKey: "fresh player", Kind: dictionaryupdate.SourceKindRoster, Position: "TE", Number: "88"},
	}
	newerSnap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200, newerEntities, nil, time.Now())
	if _, err := dictionarysnapshot.Write(dir, newerSnap, time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("write newer snapshot: %v", err)
	}

	out, err := testsupport.Execute(t, "dictionary", "diff", "--dictionary", dictPath, "--snapshot-dir", dir, "--format", "json")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}

	var proposals []dictionaryupdate.Proposal
	if err := json.Unmarshal([]byte(out), &proposals); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}

	addEntityKeys := make(map[string]bool)
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalAddEntity {
			addEntityKeys[p.CanonicalKey] = true
		}
	}
	if addEntityKeys["stale player"] {
		t.Errorf("expected 'stale player' from the OLDER snapshot to be ignored, got proposals: %+v", proposals)
	}
	if !addEntityKeys["fresh player"] {
		t.Errorf("expected 'fresh player' from the NEWER snapshot to be proposed, got proposals: %+v", proposals)
	}
}
