package sqlite_test

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-sqlite-test-template-")
	if err != nil {
		panic("create SQLite test template directory: " + err.Error())
	}

	migratedTestDBTemplate = filepath.Join(templateDir, "migrated.db")
	db, err := infrasqlite.Open(migratedTestDBTemplate, 5*time.Second)
	if err != nil {
		_ = os.RemoveAll(templateDir)
		panic("open SQLite test template: " + err.Error())
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("migrate SQLite test template: " + err.Error())
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("checkpoint SQLite test template: " + err.Error())
	}
	if err := db.Close(); err != nil {
		_ = os.RemoveAll(templateDir)
		panic("close SQLite test template: " + err.Error())
	}

	exitCode := m.Run()
	_ = os.RemoveAll(templateDir)
	os.Exit(exitCode)
}

// mustID builds a source.ID for test fixtures, failing immediately if raw
// isn't a valid identifier for platform. Accepts testing.TB (rather than
// *testing.T) so both regular tests and benchmarks (*testing.B) can reuse
// it — shared by every _test.go file in this package (all package
// sqlite_test).
func mustID(t testing.TB, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}

// newTestDB accepts testing.TB (rather than *testing.T) so both regular
// tests and benchmarks (*testing.B) can reuse it — see bench_test.go.
func newTestDB(t testing.TB) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	copyMigratedTestDB(t, path)

	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func copyMigratedTestDB(t testing.TB, path string) {
	t.Helper()
	source, err := os.Open(migratedTestDBTemplate)
	if err != nil {
		t.Fatalf("open migrated SQLite test template: %v", err)
	}
	defer source.Close()

	destination, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create SQLite test database: %v", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatalf("copy migrated SQLite test template: %v", err)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("close copied SQLite test database: %v", err)
	}
}
