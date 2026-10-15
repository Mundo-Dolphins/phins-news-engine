package dictionary_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// writeHistoryArtifacts writes the on-disk artifact set (dictionary, diff,
// decisions, validation) for version under baseDir via
// dictionaryhistory.WriteVersion, mirroring what `dictionary update` writes
// for the outgoing version before promoting a new one.
func writeHistoryArtifacts(t *testing.T, baseDir, version string) {
	t.Helper()
	if err := dictionaryhistory.WriteVersion(baseDir, dictionaryhistory.Artifacts{
		Version:    version,
		Dictionary: dictionary.Dictionary{Version: version, Name: "test_dictionary", Language: "en"},
		Diff:       "some diff content",
	}); err != nil {
		t.Fatalf("write history artifacts: %v", err)
	}
}

// seedHistoryDB copies the empty migrated template and records one
// dictionary_history row.
func seedHistoryDB(t *testing.T, dbPath string) {
	t.Helper()
	testsupport.CopyMigratedTestDB(t, dbPath)
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewDictionaryHistoryRepository(db)
	if err := repo.Record(context.Background(), out.DictionaryHistoryRecord{
		Version: "1.0.1", ContentHash: "hash1", CreatedAt: time.Now().UTC(), ChangeCount: 2, OperationKind: "update",
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
}

func TestDictionaryHistory_ListsRecordedVersions(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	t.Chdir(filepath.Dir(cfgPath))

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "1.0.1") || !strings.Contains(out, "hash1") {
		t.Errorf("expected the seeded version listed, got:\n%s", out)
	}
}

func TestDictionaryHistory_JSONFormat(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	t.Chdir(filepath.Dir(cfgPath))

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--format", "json")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v", err)
	}
	if !strings.Contains(out, `"Version": "1.0.1"`) {
		t.Errorf("expected valid-looking JSON output, got:\n%s", out)
	}
}

func TestDictionaryHistory_MarkdownFormat(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	t.Chdir(filepath.Dir(cfgPath))

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--format", "markdown")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "1.0.1") || !strings.Contains(out, "hash1") || !strings.Contains(out, "|") {
		t.Errorf("expected a markdown table with the seeded version, got:\n%s", out)
	}
}

func TestDictionaryHistory_VersionDetail_CombinesRecordAndArtifacts(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	dir := filepath.Dir(cfgPath)
	t.Chdir(dir)

	// Write the on-disk artifacts for version 1.0.1 under the same relative
	// history dir the `dictionary update` command uses, so `--version`
	// detail mode has file artifacts to combine with the SQLite record.
	writeHistoryArtifacts(t, filepath.Join(dir, "data", "dictionary", "history"), "1.0.1")

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--version", "1.0.1")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "hash1") {
		t.Errorf("expected the SQLite record's hash in detail output, got:\n%s", out)
	}
	if !strings.Contains(out, "some diff content") {
		t.Errorf("expected the on-disk diff artifact in detail output, got:\n%s", out)
	}
}

func TestDictionaryHistory_VersionDetail_UnknownVersionFails(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	t.Chdir(filepath.Dir(cfgPath))

	_, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--version", "9.9.9")
	if err == nil {
		t.Fatal("testsupport.Execute: error = nil, want error for an unknown version")
	}
}

func TestDictionaryHistory_VersionDetail_JSONFormat(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	dir := filepath.Dir(cfgPath)
	t.Chdir(dir)
	writeHistoryArtifacts(t, filepath.Join(dir, "data", "dictionary", "history"), "1.0.1")

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--version", "1.0.1", "--format", "json")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, `"Version": "1.0.1"`) {
		t.Errorf("expected the SQLite record's version in JSON detail output, got:\n%s", out)
	}
	if !strings.Contains(out, "some diff content") {
		t.Errorf("expected the on-disk diff artifact in JSON detail output, got:\n%s", out)
	}
}

func TestDictionaryHistory_VersionDetail_MarkdownFormat(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	dir := filepath.Dir(cfgPath)
	t.Chdir(dir)
	writeHistoryArtifacts(t, filepath.Join(dir, "data", "dictionary", "history"), "1.0.1")

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--version", "1.0.1", "--format", "markdown")
	if err != nil {
		t.Fatalf("testsupport.Execute: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "hash1") || !strings.Contains(out, "|") {
		t.Errorf("expected a markdown table with the SQLite record's hash in detail output, got:\n%s", out)
	}
	if !strings.Contains(out, "some diff content") {
		t.Errorf("expected the on-disk diff artifact in markdown detail output, got:\n%s", out)
	}
}

func TestDictionaryHistory_InvalidFormat_ListModeFails(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	t.Chdir(filepath.Dir(cfgPath))

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--format", "bogus")
	if err == nil {
		t.Fatal("testsupport.Execute: error = nil, want error for an unrecognized --format")
	}
	if !strings.Contains(err.Error(), `unknown --format "bogus"`) {
		t.Errorf("testsupport.Execute: error = %v, want it to name the invalid --format value\noutput:\n%s", err, out)
	}
}

func TestDictionaryHistory_InvalidFormat_DetailModeFails(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	seedHistoryDB(t, dbPath)
	dir := filepath.Dir(cfgPath)
	t.Chdir(dir)
	writeHistoryArtifacts(t, filepath.Join(dir, "data", "dictionary", "history"), "1.0.1")

	out, err := testsupport.Execute(t, "--config", cfgPath, "dictionary", "history", "--version", "1.0.1", "--format", "bogus")
	if err == nil {
		t.Fatal("testsupport.Execute: error = nil, want error for an unrecognized --format")
	}
	if !strings.Contains(err.Error(), `unknown --format "bogus"`) {
		t.Errorf("testsupport.Execute: error = %v, want it to name the invalid --format value\noutput:\n%s", err, out)
	}
}
