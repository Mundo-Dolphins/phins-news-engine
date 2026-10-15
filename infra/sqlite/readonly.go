package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// OpenReadOnly opens the SQLite database at path in read-only mode
// (URI mode=ro) — used by editorial cycle's --dry-run planning phase
// so a plan preview can never write a session, label, or any
// other row. Unlike Open, it does not set journal_mode(WAL) or
// synchronous(NORMAL) (both are write-affecting pragmas rejected or
// meaningless on a read-only connection); busy_timeout and foreign_keys
// still apply to reads. Fails fast if path does not exist — sql.Open
// itself is lazy, but this function's own internal Ping call (not any
// query the caller later issues) surfaces "unable to open database
// file" immediately, before OpenReadOnly even returns, which callers
// should treat as a legitimate "missing requirement" to report, not a
// crash.
//
// The dsn must carry the "file:" scheme: modernc.org/sqlite's newConn
// strips the query string from the DSN before opening unless it is
// prefixed with "file:" (see its conn.go), and it always passes
// SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE as the open flags regardless
// of DSN — so without "file:", mode=ro would never reach the
// underlying SQLite C library's own URI parser, and this would silently
// hand back a writable, auto-creating connection.
func OpenReadOnly(path string, busyTimeout time.Duration) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)&mode=ro",
		path, busyTimeout.Milliseconds(),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open read-only %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open read-only %s: %w", path, err)
	}
	return db, nil
}
