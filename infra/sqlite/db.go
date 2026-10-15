// Package sqlite provides the low-level SQLite connection and migration
// primitives shared by the outbound repository adapters.
package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Open opens (and, if needed, creates) the SQLite database at path with
// WAL journaling, foreign keys enabled, synchronous=NORMAL,
// temp_store=MEMORY, and the given busy timeout.
// A single connection is kept open: SQLite allows only one writer at a
// time, and a small single-board host has no benefit from a larger pool.
// A database file that does not exist yet is created 0600 first (SQLite
// gives its -wal and -shm files the same mode); an existing file keeps
// its mode.
func Open(path string, busyTimeout time.Duration) (*sql.DB, error) {
	createPrivateFile(path)
	dsn := fmt.Sprintf(
		"%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"+
			"&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)",
		path, busyTimeout.Milliseconds(),
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	return db, nil
}

// createPrivateFile creates an empty 0600 file at path when nothing exists
// there yet. In-memory and URI-style names are left to the driver, and
// any failure (a missing directory, say) is left for SQLite to report.
func createPrivateFile(path string) {
	if path == "" || strings.HasPrefix(path, ":") || strings.HasPrefix(path, "file:") {
		return
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is the operator-configured database path
	if err == nil {
		_ = f.Close()
	}
}
