package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestOpen_CreatesPrivateDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	db, err := sqlite.Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, path, 0o600)
}

func TestOpen_KeepsExistingFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, path, 0o640)
}
