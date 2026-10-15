package sqlite_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func TestOpen_AppliesPragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var synchronous int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("query synchronous: %v", err)
	}
	if synchronous != 1 { // 1 = NORMAL
		t.Errorf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}

	var tempStore int
	if err := db.QueryRow(`PRAGMA temp_store`).Scan(&tempStore); err != nil {
		t.Fatalf("query temp_store: %v", err)
	}
	if tempStore != 2 { // 2 = MEMORY
		t.Errorf("temp_store = %d, want 2 (MEMORY)", tempStore)
	}
}
