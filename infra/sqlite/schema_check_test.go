package sqlite_test

import (
	"context"
	"embed"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	aheadfixture "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite/testdata/schemacheck/ahead"
	knownfixture "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite/testdata/schemacheck/known"
)

func migratedFixtureDB(t *testing.T, migrationsFS embed.FS) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db, migrationsFS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return path
}

func TestCheckSchemaCompatible_SameMigrationSet_Passes(t *testing.T) {
	path := migratedFixtureDB(t, knownfixture.FS)
	if err := sqlite.CheckSchemaCompatible(context.Background(), path, knownfixture.FS); err != nil {
		t.Errorf("CheckSchemaCompatible() error = %v, want nil", err)
	}
}

func TestCheckSchemaCompatible_SnapshotAheadOfBinary_Rejected(t *testing.T) {
	incomingPath := migratedFixtureDB(t, aheadfixture.FS)
	err := sqlite.CheckSchemaCompatible(context.Background(), incomingPath, knownfixture.FS)
	if err == nil {
		t.Fatal("CheckSchemaCompatible() error = nil, want a rejection for the unknown 0002_future.sql migration")
	}
}

func TestCheckSchemaCompatible_NeverMigrated_Passes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unmigrated.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE placeholder (id INTEGER)`); err != nil {
		t.Fatalf("seed placeholder table: %v", err)
	}
	if err := sqlite.CheckSchemaCompatible(context.Background(), path, knownfixture.FS); err != nil {
		t.Errorf("CheckSchemaCompatible() error = %v, want nil for a database with no schema_migrations table", err)
	}
}
