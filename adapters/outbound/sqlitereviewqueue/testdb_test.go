// adapters/outbound/sqlitereviewqueue/testdb_test.go
package sqlitereviewqueue_test

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-sqlitereviewqueue-test-template-")
	if err != nil {
		panic(err)
	}
	migratedTestDBTemplate = filepath.Join(templateDir, "migrated.db")
	db, err := infrasqlite.Open(migratedTestDBTemplate, 5*time.Second)
	if err != nil {
		panic(err)
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		_ = db.Close()
		panic(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		panic(err)
	}
	if err := db.Close(); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(templateDir)
	os.Exit(code)
}

func newTestDB(t testing.TB) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	src, err := os.Open(migratedTestDBTemplate)
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		t.Fatalf("copy template: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close db copy: %v", err)
	}
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
