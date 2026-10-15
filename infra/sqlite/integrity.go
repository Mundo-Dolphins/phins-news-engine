package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Integrity runs PRAGMA integrity_check and PRAGMA foreign_key_check
// against db. A clean database returns nil.
func Integrity(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("sqlite: integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite: integrity_check failed: %s", result)
	}

	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("sqlite: foreign_key_check: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		return fmt.Errorf("sqlite: foreign_key_check found violations")
	}
	return rows.Err()
}
