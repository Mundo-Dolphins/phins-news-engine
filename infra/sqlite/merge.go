package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// localOwnedTables are only ever written by the phins-news-engine-tools
// binary (interactive review, dictionary mining, training) — never by
// the production phins-news-engine binary, even though both share the exact
// same migrations.FS and so both have these tables. A synced snapshot's
// copy of them is always empty (production never writes them); this
// list is deleted and replaced wholesale with the previous local
// database's rows, never unioned.
var localOwnedTables = []string{
	"editorial_cycle_sessions",
	"editorial_review_decisions",
	"human_labels",
	"editorial_examples",
	"human_label_conflicts",
	"dictionary_candidate_terms",
	"dictionary_history",
	// Operator decisions on event rule proposals (migration 0045): written
	// only by the tools' event rules step, never by production.
	"event_rule_proposals",
}

// optionalPreviousTables are locally-owned tables a previous local database
// may predate (it was never migrated past their migration). There is
// nothing to carry forward from such a database, so the copy is skipped
// instead of failing the sync.
var optionalPreviousTables = map[string]bool{
	"event_rule_proposals": true,
}

// registryTables are written independently by both binaries — production
// self-registers the model/dictionary it loaded at startup
// (engine/run.go), and the tools binary registers what it
// trains/promotes. Each row is keyed by a natural TEXT primary key
// (version/id), so previous's rows are unioned into the snapshot's own
// copy (INSERT OR IGNORE) rather than replacing it.
var registryTables = []string{
	"model_registry",
	"dictionary_registry",
	"training_runs",
}

// offlineLLMTables hold the output of the offline LLM runs (`editorial
// classify`, `entities discover`) plus the operator's review decisions on
// it. Rerunning those LLM jobs takes hours, so a sync must not drop them.
// They are unioned (INSERT OR IGNORE) rather than replaced because
// production's online entity fallback (entity_extraction.fallback) also
// writes entity_proposals. All keys are TEXT natural keys, so
// a row present on both sides keeps the snapshot's copy.
var offlineLLMTables = []string{
	"editorial_label_proposals",
	"editorial_label_analysis_processed_articles",
	"entity_proposals",
	"entity_proposal_aliases",
	"entity_proposal_sources",
	"entity_proposal_web_sources",
	"entity_discovery_processed_articles",
}

// MergeLocalOwnedTables carries the previous local database's
// locally-owned and registry table rows forward into db (a freshly
// promoted, freshly opened snapshot), so a sync never destroys the
// editorial review corpus the remote database never had. It runs with
// foreign key enforcement off — a locally-reviewed article can
// legitimately have aged out of the new snapshot's articles table, and
// this must not block the merge — then reports (does not fail on) any
// resulting foreign_key_check violations, mirroring migrate.go's
// applyMigration precedent for the same PRAGMA toggle and the same
// "some rows outliving their source article is expected" reasoning
// editorial_examples (#155) was built around.
func MergeLocalOwnedTables(ctx context.Context, db *sql.DB, previousPath string) ([]string, error) {
	if _, err := os.Stat(previousPath); err != nil {
		return nil, fmt.Errorf("sqlite: merge: previous database %s: %w", previousPath, err)
	}

	if _, err := db.ExecContext(ctx, `ATTACH DATABASE ? AS previous`, previousPath); err != nil {
		return nil, fmt.Errorf("sqlite: merge: attach %s: %w", previousPath, err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DETACH DATABASE previous`) }()

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return nil, fmt.Errorf("sqlite: merge: disable foreign keys: %w", err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `PRAGMA foreign_keys=ON`) }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: merge: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, table := range localOwnedTables {
		// #nosec G201 -- table is always one of the hardcoded literals in
		// localOwnedTables above, never external input.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM main.%s`, table)); err != nil {
			return nil, fmt.Errorf("sqlite: merge: clear %s: %w", table, err)
		}
		if err := copyPreviousTable(ctx, tx, table, false); err != nil {
			return nil, fmt.Errorf("sqlite: merge: copy %s: %w", table, err)
		}
	}
	for _, table := range append(append([]string{}, registryTables...), offlineLLMTables...) {
		// #nosec G201 -- table is always one of the hardcoded literals in
		// registryTables/offlineLLMTables above, never external input.
		if err := copyPreviousTable(ctx, tx, table, true); err != nil {
			return nil, fmt.Errorf("sqlite: merge: union %s: %w", table, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlite: merge: commit: %w", err)
	}

	var warnings []string
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: merge: foreign_key_check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, refTable string
		var rowid sql.NullInt64
		var fkIndex int
		if err := rows.Scan(&table, &rowid, &refTable, &fkIndex); err != nil {
			return nil, fmt.Errorf("sqlite: merge: scan foreign_key_check row: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf("%s row (rowid=%v) references missing %s row", table, rowid, refTable))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: merge: foreign_key_check: %w", err)
	}
	return warnings, nil
}

// copyPreviousTable names the source columns explicitly so an older local
// database can be copied into a migrated snapshot. New destination columns
// retain their defaults; the backup is never migrated or otherwise changed.
func copyPreviousTable(ctx context.Context, tx *sql.Tx, table string, ignore bool) error {
	// #nosec G201 -- table comes from the hardcoded table lists above.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA previous.table_info(%s)`, table))
	if err != nil {
		return err
	}
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		columns = append(columns, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		if optionalPreviousTables[table] {
			return nil
		}
		return fmt.Errorf("previous database is missing table %s", table)
	}
	verb := "INSERT"
	if ignore {
		verb = "INSERT OR IGNORE"
	}
	columnList := strings.Join(columns, ", ")
	// #nosec G201 -- table and verb are hardcoded; column names are quoted SQLite identifiers.
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`%s INTO main.%s (%s) SELECT %s FROM previous.%s`, verb, table, columnList, columnList, table))
	return err
}
