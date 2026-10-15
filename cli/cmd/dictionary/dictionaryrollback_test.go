package dictionary_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// validRollbackDecision is a Decision that passes dictionary.Validate with no
// error-severity issues — WriteCandidate revalidates the restored content, so
// the fixture dictionaries must carry a real Decision (a zero Decision fails
// validateDecision's max_count_per_term/title_weight_multiplier checks).
var validRollbackDecision = dictionary.Decision{
	PositiveThresholdHigh:   3,
	PositiveThresholdMedium: 1,
	NegativeThresholdHigh:   0,
	TitleWeightMultiplier:   1,
	MaxCountPerTerm:         3,
}

func rollbackOldDict() dictionary.Dictionary {
	return dictionary.Dictionary{
		Version: "1.0.0", Name: "test_dictionary", Language: "en",
		Decision: validRollbackDecision,
		Categories: []dictionary.Category{{
			Name: "active_roster_core", Weight: 1,
			Entries: []dictionary.Entry{
				{CanonicalName: "Springfield Isotopes", Source: "roster", Confidence: 0.5},
			},
		}},
	}
}

func rollbackCurrentDict() dictionary.Dictionary {
	return dictionary.Dictionary{
		Version: "1.0.1", Name: "test_dictionary", Language: "en",
		Decision: validRollbackDecision,
		Categories: []dictionary.Category{{
			Name: "active_roster_core", Weight: 1,
			Entries: []dictionary.Entry{
				{CanonicalName: "Springfield Isotopes", Source: "roster", Confidence: 0.5},
				{CanonicalName: "Some New Entity", Source: "roster", Confidence: 0.5},
			},
		}},
	}
}

// seedRollbackFixture writes an operational DB (migrated) with a single
// dictionary_history row for version 1.0.0, the on-disk history artifacts for
// 1.0.0, and a current on-disk dictionary at version 1.0.1 (one extra entry).
// It returns the db and dictionary file paths.
func seedRollbackFixture(t *testing.T, dir string) (dbPath, dictPath string) {
	t.Helper()
	dbPath = filepath.Join(dir, "test.db")
	testsupport.CopyMigratedTestDB(t, dbPath)
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	oldDict := rollbackOldDict()
	if err := dictionaryhistory.WriteVersion(filepath.Join(dir, "data", "dictionary", "history"), dictionaryhistory.Artifacts{
		Version: "1.0.0", Dictionary: oldDict,
	}); err != nil {
		t.Fatalf("write history: %v", err)
	}
	repo := sqlite.NewDictionaryHistoryRepository(db)
	if err := repo.Record(context.Background(), out.DictionaryHistoryRecord{
		Version: "1.0.0", ContentHash: dictionary.ContentHash(oldDict), CreatedAt: time.Now().UTC(),
		OperationKind: "update",
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	dictPath = filepath.Join(dir, "dictionary.yml")
	if err := dictionaryfile.Save(dictPath, rollbackCurrentDict()); err != nil {
		t.Fatalf("save current dict: %v", err)
	}
	return dbPath, dictPath
}

// writeRollbackTestConfig writes a full, valid settings.yml (reusing the
// package fixture template) with the sqlite and dictionary paths pointed at the
// seeded fixture.
func writeRollbackTestConfig(t *testing.T, dir, dbPath, dictPath string) string {
	t.Helper()
	contents := strings.Replace(
		fmt.Sprintf(testsupport.FixtureTemplate, dbPath),
		"dictionary:\n  path: \"\"\n  version: \"\"",
		"dictionary:\n  path: "+dictPath+"\n  version: \"1.0.1\"",
		1,
	)
	path := filepath.Join(dir, "settings.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestDictionaryRollback_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	dbPath, dictPath := seedRollbackFixture(t, dir)
	t.Chdir(dir)
	cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "rollback", "--to", "1.0.0", "--dry-run")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}

	d, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Version != "1.0.1" {
		t.Fatalf("--dry-run must not modify the dictionary, version is now %q", d.Version)
	}
}

func TestDictionaryRollback_RestoresContentAsNewVersion(t *testing.T) {
	dir := t.TempDir()
	dbPath, dictPath := seedRollbackFixture(t, dir)
	t.Chdir(dir)
	cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "rollback", "--to", "1.0.0", "--non-interactive")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "1.0.1 -> 1.0.2") {
		t.Errorf("expected a new version (1.0.2), not a rewind to 1.0.0, got:\n%s", out)
	}

	d, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Version != "1.0.2" {
		t.Fatalf("Version = %q, want 1.0.2 (a NEW version whose content matches 1.0.0)", d.Version)
	}
	if len(d.Categories[0].Entries) != 1 {
		t.Fatalf("expected content restored to the 1.0.0 shape (1 entry), got %d entries", len(d.Categories[0].Entries))
	}
}

func TestDictionaryRollback_InteractiveConfirmationApplies(t *testing.T) {
	dir := t.TempDir()
	dbPath, dictPath := seedRollbackFixture(t, dir)
	t.Chdir(dir)
	cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)

	out, err := testsupport.ExecuteWithStdin(t, "y\n", "--config", cfgPath, "dictionary", "rollback", "--to", "1.0.0")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	d, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Version != "1.0.2" {
		t.Fatalf("Version = %q, want 1.0.2 after confirming with 'y'", d.Version)
	}
}

func TestDictionaryRollback_InteractiveDeclineWritesNothing(t *testing.T) {
	dir := t.TempDir()
	dbPath, dictPath := seedRollbackFixture(t, dir)
	t.Chdir(dir)
	cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)

	out, err := testsupport.ExecuteWithStdin(t, "n\n", "--config", cfgPath, "dictionary", "rollback", "--to", "1.0.0")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "aborted") {
		t.Errorf("expected an abort message when declining, got:\n%s", out)
	}
	d, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Version != "1.0.1" {
		t.Fatalf("declining must not modify the dictionary, version is now %q", d.Version)
	}
}

func TestDictionaryRollback_ChecksumMismatchRefuses(t *testing.T) {
	dir := t.TempDir()
	dbPath, dictPath := seedRollbackFixture(t, dir)
	t.Chdir(dir)
	cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)

	// Corrupt the recorded checksum for 1.0.0 so it no longer matches the
	// on-disk artifact content — rollback must refuse.
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	repo := sqlite.NewDictionaryHistoryRepository(db)
	if err := repo.Record(context.Background(), out.DictionaryHistoryRecord{
		Version: "1.0.0", ContentHash: "not-the-real-hash", CreatedAt: time.Now().UTC(), OperationKind: "update",
	}); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}
	db.Close()

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "rollback", "--to", "1.0.0", "--non-interactive")
	if err == nil {
		t.Fatalf("expected an error on checksum mismatch, got none\noutput:\n%s", out)
	}
	if !strings.Contains(err.Error(), "checksum") && !strings.Contains(err.Error(), "does not match") {
		t.Errorf("expected a checksum-mismatch error, got: %v", err)
	}
	d, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Version != "1.0.1" {
		t.Fatalf("a refused rollback must not modify the dictionary, version is now %q", d.Version)
	}
}

func TestDictionaryRollback_PreviousFlag(t *testing.T) {
	dir := t.TempDir()
	dbPath, dictPath := seedRollbackFixture(t, dir)
	t.Chdir(dir)
	cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "rollback", "--previous", "--force")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	d, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Version != "1.0.2" {
		t.Fatalf("Version = %q, want 1.0.2 after --previous rollback to 1.0.0's content", d.Version)
	}
	if len(d.Categories[0].Entries) != 1 {
		t.Fatalf("expected 1 entry after rolling back to 1.0.0, got %d", len(d.Categories[0].Entries))
	}
}

func TestDictionaryRollback_LedgerRecordsRollbackAndPreservesHistory(t *testing.T) {
	dir := t.TempDir()
	dbPath, dictPath := seedRollbackFixture(t, dir)
	t.Chdir(dir)
	cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)

	if out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "rollback", "--to", "1.0.0", "--non-interactive"); err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}

	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewDictionaryHistoryRepository(db)
	list, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byVersion := make(map[string]out.DictionaryHistoryRecord, len(list))
	for _, r := range list {
		byVersion[r.Version] = r
	}

	// The target's own row (an earlier version) is untouched.
	if _, ok := byVersion["1.0.0"]; !ok {
		t.Errorf("earlier version 1.0.0's history row was destroyed")
	}
	// The outgoing version was backed up as its own ledger row.
	if _, ok := byVersion["1.0.1"]; !ok {
		t.Errorf("outgoing version 1.0.1 was not recorded in the ledger")
	}
	// The rollback produced a NEW version with the right operation metadata.
	newRec, ok := byVersion["1.0.2"]
	if !ok {
		t.Fatalf("rolled-back-to version 1.0.2 not recorded, ledger has: %v", byVersion)
	}
	if newRec.OperationKind != "rollback" {
		t.Errorf("OperationKind = %q, want %q", newRec.OperationKind, "rollback")
	}
	if newRec.PredecessorVersion != "1.0.1" {
		t.Errorf("PredecessorVersion = %q, want %q", newRec.PredecessorVersion, "1.0.1")
	}
	if !newRec.Promoted {
		t.Errorf("expected the new version 1.0.2 to be promoted")
	}
	if byVersion["1.0.0"].Promoted || byVersion["1.0.1"].Promoted {
		t.Errorf("only the new version should be promoted after rollback")
	}

	// The target's on-disk artifacts are still intact and unchanged.
	art, err := dictionaryhistory.ReadVersion(filepath.Join(dir, "data", "dictionary", "history"), "1.0.0")
	if err != nil {
		t.Fatalf("ReadVersion 1.0.0: %v", err)
	}
	if len(art.Dictionary.Categories[0].Entries) != 1 {
		t.Errorf("1.0.0 artifact content was altered")
	}
}
