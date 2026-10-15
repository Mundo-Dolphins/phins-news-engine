package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
)

// CheckSchemaCompatible opens the SQLite database at path with a plain
// connection (no WAL pragma — this is a one-shot read, and switching a
// database into WAL mode leaves -wal/-shm sidecars this check has no
// business creating) and fails if its schema_migrations table records a
// version this binary's migrationsFS does not know about: i.e. it
// refuses to promote a snapshot produced by a newer build than this
// tools binary understands. A database with no schema_migrations table
// at all (never migrated) is always compatible — Migrate creates the
// table and applies every migration to it, same as any fresh database.
func CheckSchemaCompatible(ctx context.Context, path string, migrationsFS embed.FS) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("sqlite: schema check: open %s: %w", path, err)
	}
	defer db.Close()

	var tableExists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&tableExists); err != nil {
		return fmt.Errorf("sqlite: schema check: %w", err)
	}
	if tableExists == 0 {
		return nil
	}

	known, err := migrationNames(migrationsFS)
	if err != nil {
		return err
	}
	knownSet := make(map[string]bool, len(known))
	for _, name := range known {
		knownSet[name] = true
	}

	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("sqlite: schema check: read schema_migrations: %w", err)
	}
	defer rows.Close()

	var unknown []string
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("sqlite: schema check: scan version: %w", err)
		}
		if !knownSet[version] {
			unknown = append(unknown, version)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: schema check: %w", err)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("sqlite: schema check: snapshot has migration(s) %v this build does not know — it was produced by a newer build; refusing to promote", unknown)
	}
	return nil
}

// PendingMigrations returns the migration file names in migrationsFS
// that schema_migrations does not yet record as applied, without
// applying them — the read-only counterpart to Migrate, used by
// editorial cycle's --dry-run planning phase to surface a
// stale schema as a detectable, non-mutating warning. A database with
// no schema_migrations table at all reports every known migration as
// pending (mirrors CheckSchemaCompatible's own "never migrated" case).
func PendingMigrations(ctx context.Context, db *sql.DB, migrationsFS embed.FS) ([]string, error) {
	known, err := migrationNames(migrationsFS)
	if err != nil {
		return nil, err
	}

	var tableExists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&tableExists); err != nil {
		return nil, fmt.Errorf("sqlite: pending migrations: %w", err)
	}
	if tableExists == 0 {
		return known, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: pending migrations: read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("sqlite: pending migrations: scan version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: pending migrations: %w", err)
	}

	var pending []string
	for _, name := range known {
		if !applied[name] {
			pending = append(pending, name)
		}
	}
	return pending, nil
}
