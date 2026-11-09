package dictionarywiring

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var rollbackDecision = dictionary.Decision{
	PositiveThresholdHigh: 3, PositiveThresholdMedium: 1, TitleWeightMultiplier: 1, MaxCountPerTerm: 3,
}

func rollbackDict(version string, names ...string) dictionary.Dictionary {
	entries := make([]dictionary.Entry, 0, len(names))
	for _, n := range names {
		entries = append(entries, dictionary.Entry{CanonicalName: n, Source: "roster", Confidence: 0.5})
	}
	return dictionary.Dictionary{
		Version: version, Name: "test_dictionary", Language: "en", Decision: rollbackDecision,
		Categories: []dictionary.Category{{Name: "active_roster_core", Weight: 1, Entries: entries}},
	}
}

func migratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "t.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedRollback chdirs into a temp dir (history artifacts live under the
// relative dictionaryhistory.DefaultDir), records history versions 1.0.0 and
// 1.0.1 with their artifacts, and writes the current dictionary at 1.0.1.
func seedRollback(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	db := migratedDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, d := range []dictionary.Dictionary{
		rollbackDict("1.0.0", "Springfield Isotopes"),
		rollbackDict("1.0.1", "Springfield Isotopes", "Some New Entity"),
	} {
		if err := dictionaryhistory.WriteVersion(dictionaryhistory.DefaultDir, dictionaryhistory.Artifacts{Version: d.Version, Dictionary: d}); err != nil {
			t.Fatal(err)
		}
		if err := repo.Record(context.Background(), out.DictionaryHistoryRecord{
			Version: d.Version, ContentHash: dictionary.ContentHash(d), CreatedAt: base.Add(time.Duration(i) * time.Hour), OperationKind: "update",
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Dictionary.Path = filepath.Join(dir, "dictionary.yml")
	if err := dictionaryfile.Save(cfg.Dictionary.Path, rollbackDict("1.0.1", "Springfield Isotopes", "Some New Entity")); err != nil {
		t.Fatal(err)
	}
	return db, cfg
}

func historyRows(t *testing.T, db *sql.DB) []out.DictionaryHistoryRecord {
	t.Helper()
	list, err := History(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var rollbackNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestRollbackArgumentErrors(t *testing.T) {
	for _, tc := range []struct {
		opts RollbackOptions
		want string
	}{
		{RollbackOptions{}, "--to <version> or --previous is required"},
		{RollbackOptions{To: "1.0.0", Previous: true}, "--to and --previous are mutually exclusive"},
	} {
		_, err := Rollback(context.Background(), nil, &config.Config{}, tc.opts, rollbackNow)
		if err == nil || err.Error() != tc.want {
			t.Errorf("Rollback(%+v) error = %v, want %q", tc.opts, err, tc.want)
		}
	}
}

func TestRollbackChecksumMismatch(t *testing.T) {
	db, cfg := seedRollback(t)
	// Tamper with the stored 1.0.0 artifact so it no longer matches the
	// checksum recorded in the ledger.
	if err := dictionaryhistory.WriteVersion(dictionaryhistory.DefaultDir, dictionaryhistory.Artifacts{
		Version: "1.0.0", Dictionary: rollbackDict("1.0.0", "Tampered Entity"),
	}); err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, cfg.Dictionary.Path)
	rowsBefore := historyRows(t, db)

	for _, dryRun := range []bool{true, false} {
		_, err := Rollback(context.Background(), db, cfg, RollbackOptions{To: "1.0.0", DryRun: dryRun}, rollbackNow)
		if !errors.Is(err, ErrRollbackChecksum) {
			t.Fatalf("DryRun=%v: error = %v, want ErrRollbackChecksum", dryRun, err)
		}
		if want := "stored content for version 1.0.0 does not match its recorded checksum — refusing to restore"; err.Error() != want {
			t.Errorf("error text = %q, want %q", err.Error(), want)
		}
	}
	if after := readBytes(t, cfg.Dictionary.Path); !bytes.Equal(before, after) {
		t.Error("dictionary file changed after a refused rollback")
	}
	if rowsAfter := historyRows(t, db); len(rowsAfter) != len(rowsBefore) {
		t.Errorf("dictionary_history rows = %d, want %d", len(rowsAfter), len(rowsBefore))
	}
}

func TestRollbackDryRunWritesNothing(t *testing.T) {
	db, cfg := seedRollback(t)
	before := readBytes(t, cfg.Dictionary.Path)
	rowsBefore := historyRows(t, db)

	res, err := Rollback(context.Background(), db, cfg, RollbackOptions{To: "1.0.0", DryRun: true}, rollbackNow)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.From != "1.0.1" || res.RestoredFrom != "1.0.0" || res.Written != "" {
		t.Errorf("result = %+v", res)
	}
	if want := "Possible departures (1):\n  - some new entity: absent for 1 consecutive successful run(s) over 0 day(s)\nUnchanged: 1\n"; res.DiffText != want {
		t.Errorf("DiffText = %q, want %q", res.DiffText, want)
	}
	if len(res.Proposals) != 2 || res.Regression != nil || res.RegressionErr != nil {
		t.Errorf("proposals = %d, regression = %v / %v", len(res.Proposals), res.Regression, res.RegressionErr)
	}
	if after := readBytes(t, cfg.Dictionary.Path); !bytes.Equal(before, after) {
		t.Error("dictionary file changed by a dry run")
	}
	if rowsAfter := historyRows(t, db); len(rowsAfter) != len(rowsBefore) {
		t.Errorf("dictionary_history rows = %d, want %d", len(rowsAfter), len(rowsBefore))
	}
}

func TestRollbackPreviousPicksMostRecentOther(t *testing.T) {
	db, cfg := seedRollback(t)
	// 1.0.1 is the newest row but it is the version on disk, so --previous
	// must pick 1.0.0.
	res, err := Rollback(context.Background(), db, cfg, RollbackOptions{Previous: true, DryRun: true}, rollbackNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.RestoredFrom != "1.0.0" {
		t.Errorf("RestoredFrom = %q, want 1.0.0", res.RestoredFrom)
	}
}

func TestRollbackPreviousWithoutOtherVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	db := migratedDB(t)
	cfg := &config.Config{}
	cfg.Dictionary.Path = filepath.Join(t.TempDir(), "dictionary.yml")
	d := rollbackDict("1.0.1", "Springfield Isotopes")
	if err := dictionaryfile.Save(cfg.Dictionary.Path, d); err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.NewDictionaryHistoryRepository(db).Record(context.Background(), out.DictionaryHistoryRecord{
		Version: "1.0.1", ContentHash: dictionary.ContentHash(d), CreatedAt: rollbackNow, OperationKind: "update",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := Rollback(context.Background(), db, cfg, RollbackOptions{Previous: true}, rollbackNow)
	if !errors.Is(err, ErrNoPreviousVersion) || err.Error() != "no previous version found" {
		t.Fatalf("error = %v, want ErrNoPreviousVersion", err)
	}
}

func TestRollbackUnknownVersionIsNoRows(t *testing.T) {
	db, cfg := seedRollback(t)
	_, err := Rollback(context.Background(), db, cfg, RollbackOptions{To: "9.9.9", DryRun: true}, rollbackNow)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error = %v, want sql.ErrNoRows", err)
	}
}

func TestRollbackRecordsAndPromotes(t *testing.T) {
	db, cfg := seedRollback(t)
	res, err := Rollback(context.Background(), db, cfg, RollbackOptions{To: "1.0.0"}, rollbackNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || res.From != "1.0.1" || res.Written != "1.0.2" || res.RestoredFrom != "1.0.0" {
		t.Errorf("result = %+v", res)
	}
	if res.Regression == nil && res.RegressionErr == nil {
		t.Error("neither Regression nor RegressionErr set after a real rollback")
	}
	d, err := dictionaryfile.Load(cfg.Dictionary.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "1.0.2" || len(d.Categories[0].Entries) != 1 {
		t.Fatalf("dictionary on disk = %s with %d entries", d.Version, len(d.Categories[0].Entries))
	}
	byVersion := map[string]out.DictionaryHistoryRecord{}
	for _, r := range historyRows(t, db) {
		byVersion[r.Version] = r
	}
	rec, ok := byVersion["1.0.2"]
	if !ok {
		t.Fatalf("1.0.2 not recorded: %v", byVersion)
	}
	if rec.OperationKind != "rollback" || rec.PredecessorVersion != "1.0.1" || !rec.Promoted || rec.ChangeCount != 1 {
		t.Errorf("1.0.2 record = %+v", rec)
	}
	if !rec.CreatedAt.Equal(rollbackNow) {
		t.Errorf("CreatedAt = %v, want %v", rec.CreatedAt, rollbackNow)
	}
	if byVersion["1.0.0"].Promoted || byVersion["1.0.1"].Promoted {
		t.Error("only the new version should be promoted")
	}
	art, err := dictionaryhistory.ReadVersion(dictionaryhistory.DefaultDir, "1.0.1")
	if err != nil {
		t.Fatalf("outgoing 1.0.1 artifacts: %v", err)
	}
	if art.Diff != res.DiffText {
		t.Errorf("backed-up diff = %q, want the rollback diff %q", art.Diff, res.DiffText)
	}
}
