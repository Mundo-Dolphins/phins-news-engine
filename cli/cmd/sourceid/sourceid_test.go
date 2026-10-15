package sourceid

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// TestSourceIDMigrateReport_PreMigration_ShowsPendingChanges seeds a
// database that has every migration EXCEPT 0021 applied, plus one articles
// row with a bare (pre-namespace) source_id, and asserts
// buildSourceIDMigrateReport computes the exact old->new mapping 0021 would
// apply -- without ever calling infrasqlite.Migrate with 0021 pending
// (which would apply it for real and defeat the point of a preview).
func TestSourceIDMigrateReport_PreMigration_ShowsPendingChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Bootstrap schema_migrations and pre-mark 0021 as applied so that a
	// full Migrate() call (needed to create every other table 0021 reads
	// from) skips 0021 itself, then unmark it before actually building the
	// report -- 0021 never runs in this test.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ('0021_source_id_namespace.sql')`); err != nil {
		t.Fatalf("mark 0021 applied: %v", err)
	}
	if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate (0001-0020, everything but 0021): %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO articles (id, external_id, original_url, canonical_url, source_id, source_name, source_type, domain, status, fetched_at)
		VALUES ('a1', '', 'https://x/1', 'https://x/1', 'sprisotopes', 'Springfield Isotopes', 'twitter', 'x.com', 'fetched', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = '0021_source_id_namespace.sql'`); err != nil {
		t.Fatalf("unmark 0021: %v", err)
	}

	report, err := buildSourceIDMigrateReport(ctx, db)
	if err != nil {
		t.Fatalf("buildSourceIDMigrateReport: %v", err)
	}
	if report.AlreadyApplied {
		t.Fatal("AlreadyApplied = true, want false -- 0021 has not run yet")
	}
	found := false
	for _, m := range report.Mappings {
		if m.Table == "articles" && m.OldSourceID == "sprisotopes" && m.NewSourceID == "twitter:sprisotopes" {
			found = true
		}
	}
	if !found {
		t.Errorf("Mappings = %+v, want an articles sprisotopes -> twitter:sprisotopes entry", report.Mappings)
	}

	// Confirm 0021 genuinely never ran: schema_migrations still has no row
	// for it, and the seeded row is still in its bare, pre-namespace form.
	var stillPending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = '0021_source_id_namespace.sql'`).Scan(&stillPending); err != nil {
		t.Fatalf("check schema_migrations: %v", err)
	}
	if stillPending != 0 {
		t.Fatal("0021 was recorded as applied -- buildSourceIDMigrateReport must never run it")
	}
	var rawSourceID string
	if err := db.QueryRowContext(ctx, `SELECT source_id FROM articles WHERE id = 'a1'`).Scan(&rawSourceID); err != nil {
		t.Fatalf("check articles.source_id: %v", err)
	}
	if rawSourceID != "sprisotopes" {
		t.Fatalf("articles.source_id = %q, want still-unmigrated %q", rawSourceID, "sprisotopes")
	}
}

// TestSourceIDMigrateReport_FlagsUnparseableMapping seeds a database with
// one normally-namespaceable row (source_type "twitter", parses fine post-
// migration -- no warning) and one row whose source_type is "other", which
// has no entry in domain/source/id.go's platformRules map. 0021
// would still namespace it mechanically ("other:whatever"), but
// source.ParseID rejects "other:*" outright since "other" has no format
// rule -- exactly the pre-flight gap Fix 1 closes: an operator running
// migrate-report today gets no warning that this row would come out the
// other side unreadable by every source_id-consuming read path in this
// binary.
func TestSourceIDMigrateReport_FlagsUnparseableMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ('0021_source_id_namespace.sql')`); err != nil {
		t.Fatalf("mark 0021 applied: %v", err)
	}
	if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate (0001-0020, everything but 0021): %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO articles (id, external_id, original_url, canonical_url, source_id, source_name, source_type, domain, status, fetched_at)
		VALUES ('a-ok', '', 'https://x/1', 'https://x/1', 'sprisotopes', 'Springfield Isotopes', 'twitter', 'x.com', 'fetched', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed ok article: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO articles (id, external_id, original_url, canonical_url, source_id, source_name, source_type, domain, status, fetched_at)
		VALUES ('a-bad', '', 'https://x/2', 'https://x/2', 'legacyfeed', 'Legacy Feed', 'other', 'x.com', 'fetched', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed bad article: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = '0021_source_id_namespace.sql'`); err != nil {
		t.Fatalf("unmark 0021: %v", err)
	}

	report, err := buildSourceIDMigrateReport(ctx, db)
	if err != nil {
		t.Fatalf("buildSourceIDMigrateReport: %v", err)
	}

	var okMapping, badMapping *sourceIDMapping
	for i := range report.Mappings {
		m := &report.Mappings[i]
		switch m.NewSourceID {
		case "twitter:sprisotopes":
			okMapping = m
		case "other:legacyfeed":
			badMapping = m
		}
	}
	if okMapping == nil {
		t.Fatalf("Mappings = %+v, want a twitter:sprisotopes entry", report.Mappings)
	}
	if okMapping.Unparseable {
		t.Errorf("okMapping.Unparseable = true, want false — %q should parse cleanly: %s", okMapping.NewSourceID, okMapping.ParseError)
	}
	if badMapping == nil {
		t.Fatalf("Mappings = %+v, want an other:legacyfeed entry", report.Mappings)
	}
	if !badMapping.Unparseable {
		t.Error("badMapping.Unparseable = false, want true — \"other\" has no platformRules entry, so source.ParseID must reject it")
	}
	if badMapping.ParseError == "" {
		t.Error("badMapping.ParseError is empty, want a non-empty error message")
	}

	if got := report.UnparseableCount(); got != 1 {
		t.Errorf("UnparseableCount() = %d, want 1", got)
	}
}

// TestSourceIDMigrateReport_AlreadyApplied runs the normal, full Migrate
// (0021 included) and asserts buildSourceIDMigrateReport reports
// AlreadyApplied instead of re-deriving a (now no-op) mapping.
func TestSourceIDMigrateReport_AlreadyApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	report, err := buildSourceIDMigrateReport(ctx, db)
	if err != nil {
		t.Fatalf("buildSourceIDMigrateReport: %v", err)
	}
	if !report.AlreadyApplied {
		t.Error("AlreadyApplied = false, want true -- 0021 already ran as part of the normal Migrate above")
	}
}
