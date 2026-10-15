package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Migrate applies every *.sql file in migrationsFS that has not yet been
// recorded in the schema_migrations table, in filename order, each inside
// its own transaction.
func Migrate(ctx context.Context, db *sql.DB, migrationsFS embed.FS) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	names, err := migrationNames(migrationsFS)
	if err != nil {
		return err
	}

	for _, name := range names {
		applied, err := isApplied(ctx, db, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		contents, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("sqlite: read migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, db, name, string(contents)); err != nil {
			return err
		}
	}
	return nil
}

func migrationNames(migrationsFS embed.FS) ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("sqlite: read migrations dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func isApplied(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("sqlite: check migration %s: %w", version, err)
	}
	return count > 0, nil
}

// applyMigration runs sqlText (and records version as applied) with
// foreign key enforcement disabled around the whole operation.
//
// Why: SQLite has no ALTER TABLE support for adding or dropping a CHECK
// constraint, so a migration that needs one (e.g. adding a new
// articles.status value) has to rebuild the table — CREATE a replacement,
// copy rows in, DROP the original, RENAME the replacement into place. With
// foreign key enforcement on, DROP TABLE performs an implicit DELETE of
// every row first, and that implicit delete fires ON DELETE
// CASCADE/SET NULL exactly like a real DELETE would — silently wiping
// every row in every table that references the dropped one (for
// `articles`: article_aliases, article_processing_events,
// exact_fingerprints, source_policy_decisions, and more). PRAGMA
// foreign_keys can only be toggled outside an active transaction (it is a
// documented no-op mid-transaction), so it is set here, before BeginTx,
// and restored immediately after. This brackets every migration, not only
// ones that rebuild a table, but is a no-op for the rest: toggling
// enforcement around a plain CREATE TABLE/ADD COLUMN/CREATE INDEX has no
// observable effect, since none of those drop a referenced table.
func applyMigration(ctx context.Context, db *sql.DB, version, sqlText string) error {
	preExistingFKViolations, err := foreignKeyViolations(ctx, db)
	if err != nil {
		return fmt.Errorf("sqlite: foreign key check before migration %s: %w", version, err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("sqlite: disable foreign keys for migration %s: %w", version, err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `PRAGMA foreign_keys=ON`) }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin migration %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("sqlite: apply migration %s: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("sqlite: record migration %s: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit migration %s: %w", version, err)
	}

	// Foreign key enforcement being off for the duration of the migration
	// (above) means a migration that got the rebuild procedure wrong —
	// forgetting a row, orphaning a reference — would not fail loudly on
	// its own. This check closes that gap for violations introduced by
	// this migration while tolerating pre-existing ones, which can be a
	// legitimate artifact of carrying local editorial rows across a synced
	// remote snapshot after the referenced article aged out.
	postMigrationFKViolations, err := foreignKeyViolations(ctx, db)
	if err != nil {
		return fmt.Errorf("sqlite: foreign key check after migration %s: %w", version, err)
	}
	for violation := range postMigrationFKViolations {
		if _, existed := preExistingFKViolations[violation]; !existed {
			return fmt.Errorf("sqlite: migration %s left a dangling foreign key reference: %s", version, violation)
		}
	}
	return nil
}

func foreignKeyViolations(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	violations := make(map[string]struct{})
	for rows.Next() {
		var table, refTable string
		var rowid sql.NullInt64
		var fkIndex int
		if err := rows.Scan(&table, &rowid, &refTable, &fkIndex); err != nil {
			return nil, err
		}
		rowidText := "NULL"
		if rowid.Valid {
			rowidText = fmt.Sprintf("%d", rowid.Int64)
		}
		violations[fmt.Sprintf("%s|%s|%s|%d", table, rowidText, refTable, fkIndex)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return violations, nil
}

// MigrationStatus reports whether one migration file has been applied.
type MigrationStatus struct {
	Version string
	Applied bool
}

// Status reports, for every migration file embedded in migrationsFS, in
// filename order, whether it has been applied to db. Unlike Migrate, it
// never applies anything — it only reports state.
func Status(ctx context.Context, db *sql.DB, migrationsFS embed.FS) ([]MigrationStatus, error) {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return nil, fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	names, err := migrationNames(migrationsFS)
	if err != nil {
		return nil, err
	}

	statuses := make([]MigrationStatus, 0, len(names))
	for _, name := range names {
		applied, err := isApplied(ctx, db, name)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, MigrationStatus{Version: name, Applied: applied})
	}
	return statuses, nil
}
