package sqlite_test

import (
	"context"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func TestOpenReadOnly_RejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	rw, err := infrasqlite.Open(path, 5000)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	if err := infrasqlite.Migrate(context.Background(), rw, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rw.Close()

	ro, err := infrasqlite.OpenReadOnly(path, 5000)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	defer ro.Close()

	if _, err := ro.Exec(`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('x', datetime('now'), datetime('now'), 'running', 'review', '{}')`); err == nil {
		t.Error("write through OpenReadOnly succeeded, want it rejected")
	}
	var n int
	if err := ro.QueryRow(`SELECT COUNT(1) FROM editorial_cycle_sessions`).Scan(&n); err != nil {
		t.Errorf("read through OpenReadOnly failed: %v", err)
	}
}

func TestOpenReadOnly_MissingFile_FailsFast(t *testing.T) {
	_, err := infrasqlite.OpenReadOnly(filepath.Join(t.TempDir(), "does-not-exist.db"), 5000)
	if err == nil {
		t.Error("OpenReadOnly() on a nonexistent file succeeded, want an error")
	}
}

func TestPendingMigrations_ReportsUnappliedByName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5000)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = '0024_editorial_cycle_session_lifecycle.sql'`); err != nil {
		t.Fatalf("simulate unapplied migration: %v", err)
	}

	pending, err := infrasqlite.PendingMigrations(context.Background(), db, migrations.FS)
	if err != nil {
		t.Fatalf("PendingMigrations() error = %v", err)
	}
	if len(pending) != 1 || pending[0] != "0024_editorial_cycle_session_lifecycle.sql" {
		t.Errorf("PendingMigrations() = %v, want exactly [0024_editorial_cycle_session_lifecycle.sql]", pending)
	}
}

func TestPendingMigrations_NoSchemaMigrationsTable_ReportsEveryKnownMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5000)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// Deliberately not calling Migrate: schema_migrations does not exist
	// on this database at all yet.

	pending, err := infrasqlite.PendingMigrations(context.Background(), db, migrations.FS)
	if err != nil {
		t.Fatalf("PendingMigrations() error = %v", err)
	}

	want, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var wantNames []string
	for _, e := range want {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			wantNames = append(wantNames, e.Name())
		}
	}
	sort.Strings(wantNames)

	if !reflect.DeepEqual(pending, wantNames) {
		t.Errorf("PendingMigrations() = %v, want every known migration %v", pending, wantNames)
	}
}

func TestPendingMigrations_FullyMigrated_ReportsNonePending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5000)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pending, err := infrasqlite.PendingMigrations(context.Background(), db, migrations.FS)
	if err != nil {
		t.Fatalf("PendingMigrations() error = %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("PendingMigrations() = %v, want none pending after a full Migrate run", pending)
	}
}
