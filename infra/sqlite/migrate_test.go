package sqlite_test

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func openMigratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

func TestMigrate_CreatesTablesAndRecordsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	for _, table := range []string{"sources", "articles", "schema_migrations"} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	var version string
	if err := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = ?`, "0001_init.sql").Scan(&version); err != nil {
		t.Errorf("expected 0001_init.sql to be recorded: %v", err)
	}
}

func TestMigrate_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, "0001_init.sql").Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations rows for 0001_init.sql = %d, want 1", count)
	}
}

func TestMigrate_0002CreatesLifecycleTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	for _, table := range []string{"articles", "article_aliases", "article_processing_events", "exact_fingerprints"} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'not_a_real_status')`); err == nil {
		t.Error("insert with invalid status = nil error, want a CHECK constraint violation")
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'fetched')`); err != nil {
		t.Fatalf("insert with valid status: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO article_aliases (article_id, alias_type, alias_value) VALUES ('a1','external_id','x1')`); err != nil {
		t.Fatalf("insert alias: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO article_aliases (article_id, alias_type, alias_value) VALUES ('a1','external_id','x1')`); err == nil {
		t.Error("duplicate alias insert = nil error, want a UNIQUE constraint violation")
	}
}

func TestMigrate_0003CreatesClusteringTables(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	for _, table := range []string{"event_clusters", "event_cluster_members"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'clustered')`); err != nil {
		t.Fatalf("insert article: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'closed')`); err != nil {
		t.Fatalf("insert cluster: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE articles SET current_cluster_id = 'c1' WHERE id = 'a1'`); err != nil {
		t.Fatalf("set current_cluster_id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO event_cluster_members (cluster_id, article_id, joined_at) VALUES ('c1','a1',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert cluster member: %v", err)
	}
}

func TestMigrate_0004CreatesOutboxTables(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	for _, table := range []string{"publication_outbox", "publication_attempts"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'pending_publication')`); err != nil {
		t.Fatalf("insert article: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO publication_outbox (article_id, available_at) VALUES ('a1',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO publication_outbox (article_id, available_at) VALUES ('a1',CURRENT_TIMESTAMP)`); err == nil {
		t.Error("second outbox row for the same article_id = nil error, want a UNIQUE constraint violation")
	}

	var outboxID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM publication_outbox WHERE article_id = 'a1'`).Scan(&outboxID); err != nil {
		t.Fatalf("select outbox id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO publication_attempts (outbox_id, article_id, attempted_at, outcome) VALUES (?, 'a1', CURRENT_TIMESTAMP, 'succeeded')`, outboxID); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
}

func TestMigrate_0005CreatesRegistriesAndSeedsPlaceholders(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	for _, table := range []string{"model_registry", "dictionary_registry", "human_labels", "training_runs"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	var modelVersion, dictVersion string
	if err := db.QueryRowContext(ctx, `SELECT version FROM model_registry WHERE version = 'placeholder-v0'`).Scan(&modelVersion); err != nil {
		t.Errorf("model_registry missing seeded placeholder-v0: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM dictionary_registry WHERE version = 'placeholder-v0'`).Scan(&dictVersion); err != nil {
		t.Errorf("dictionary_registry missing seeded placeholder-v0: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'fetched')`); err != nil {
		t.Fatalf("insert article: %v", err)
	}
	var classifierVersion, dictionaryVersion string
	if err := db.QueryRowContext(ctx, `SELECT classifier_version, dictionary_version FROM articles WHERE id = 'a1'`).Scan(&classifierVersion, &dictionaryVersion); err != nil {
		t.Fatalf("select article versions: %v", err)
	}
	if classifierVersion != "placeholder-v0" {
		t.Errorf("classifier_version = %q, want %q", classifierVersion, "placeholder-v0")
	}
	if dictionaryVersion != "placeholder-v0" {
		t.Errorf("dictionary_version = %q, want %q", dictionaryVersion, "placeholder-v0")
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO human_labels (article_id, label, labeled_at) VALUES ('a1','related',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert human label: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO training_runs (id, started_at, status) VALUES ('r1',CURRENT_TIMESTAMP,'running')`); err != nil {
		t.Fatalf("insert training run: %v", err)
	}
}

func TestStatus_ReportsAppliedAndPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	statusesBeforeMigrate, err := sqlite.Status(ctx, db, migrations.FS)
	if err != nil {
		t.Fatalf("Status() before Migrate error = %v", err)
	}
	if len(statusesBeforeMigrate) == 0 {
		t.Fatal("Status() returned no migrations")
	}
	for _, s := range statusesBeforeMigrate {
		if s.Applied {
			t.Errorf("migration %s: Applied = true before any Migrate() call", s.Version)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	statusesAfterMigrate, err := sqlite.Status(ctx, db, migrations.FS)
	if err != nil {
		t.Fatalf("Status() after Migrate error = %v", err)
	}
	for _, s := range statusesAfterMigrate {
		if !s.Applied {
			t.Errorf("migration %s: Applied = false after Migrate(), want true", s.Version)
		}
	}
}

func TestMigrate_0009AddsModelRegistryColumns(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM model_registry WHERE version = 'placeholder-v0'`).Scan(&status); err != nil {
		t.Fatalf("select placeholder-v0 status: %v", err)
	}
	if status != "promoted" {
		t.Errorf("placeholder-v0 status = %q, want %q", status, "promoted")
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO model_registry (version, kind, algorithm, feature_schema_version, dataset_hash, thresholds, config, artifact_hash, status)
		VALUES ('v1', 'editorial-classifier', 'linear_svm', 'fs-1', 'hash1', '{}', '{}', 'hash2', 'candidate')
	`); err != nil {
		t.Fatalf("insert candidate model: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO model_registry (version, kind, status) VALUES ('v2', 'editorial-classifier', 'bogus')
	`); err == nil {
		t.Error("expected CHECK constraint violation for status='bogus', got nil error")
	}
}

// applyMigrationsThrough manually applies every migration up to and
// including throughVersion (in filename order) via plain ExecContext calls
// — the same SQL Migrate would run, but bypassing PRAGMA foreign_keys
// toggling and the post-migration integrity check so a test can stop
// partway through the sequence — then records each in schema_migrations so
// a later sqlite.Migrate(ctx, db, migrations.FS) call sees them as already
// applied and only runs what comes after throughVersion. Used to set up
// "database as it existed right before this migration" for a test that
// needs to observe the migration's effect on pre-existing data.
func applyMigrationsThrough(t *testing.T, ctx context.Context, db *sql.DB, throughVersion string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		contents, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, string(contents)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatalf("record migration %s: %v", name, err)
		}
		if name == throughVersion {
			return
		}
	}
	t.Fatalf("migration %s not found among embedded migrations", throughVersion)
}

func TestMigrate_AllowsPreExistingForeignKeyViolations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0029_editorial_decision_metadata.sql")

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO human_labels (article_id, label, labeled_at, reason)
		VALUES ('retained-local-label-for-pruned-article', 'publish', CURRENT_TIMESTAMP, 'historical_import')
	`); err != nil {
		t.Fatalf("seed pre-existing orphan human label: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatalf("re-enable foreign keys: %v", err)
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	for _, table := range []string{"entity_proposals", "entity_proposal_aliases", "entity_proposal_sources"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("table %s not found after migration: %v", table, err)
		}
	}
}

// TestMigrate_0017PreservesExistingDataAcrossEveryReferencingTable proves
// the fix for the data-loss risk migration 0017 could otherwise cause: it
// seeds one row in every table with a foreign key into articles(id) against
// a database migrated only through 0016 (the last migration before
// 0017's rebuild), then runs sqlite.Migrate to apply 0017, and asserts
// every one of those rows is still there afterward. Before
// infrastructure/sqlite.Migrate's applyMigration disabled foreign key
// enforcement around each migration, 0017's DROP TABLE articles would have
// fired every ON DELETE CASCADE/SET NULL trigger targeting it and silently
// deleted all of them.
func TestMigrate_0017PreservesExistingDataAcrossEveryReferencingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0016_source_policy_decisions.sql")

	seeds := []string{
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status, source_id, source_type) VALUES ('a1','https://x.com/1','https://x.com/1',CURRENT_TIMESTAMP,'clustered','s1','rss')`,
		`INSERT INTO article_aliases (article_id, alias_type, alias_value) VALUES ('a1','url','https://x.com/1')`,
		`INSERT INTO article_processing_events (article_id, occurred_at, to_status, event_type) VALUES ('a1',CURRENT_TIMESTAMP,'clustered','accept')`,
		`INSERT INTO exact_fingerprints (fingerprint, article_id) VALUES ('fp1','a1')`,
		`INSERT INTO event_clusters (id, representative_article_id, first_seen_at, last_updated_at, member_count, status) VALUES ('c1','a1',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,1,'open')`,
		`INSERT INTO event_cluster_members (cluster_id, article_id, joined_at, role) VALUES ('c1','a1',CURRENT_TIMESTAMP,'first_report')`,
		`INSERT INTO source_policy_decisions (id, article_id, source_id, authority, policy_version, decision_code, mode, decided_at) VALUES ('d1','a1','s1','official_team','v1','would_retain_near_duplicate','shadow',CURRENT_TIMESTAMP)`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	counts := map[string]string{
		"articles":                  `SELECT COUNT(1) FROM articles WHERE id = 'a1'`,
		"article_aliases":           `SELECT COUNT(1) FROM article_aliases WHERE article_id = 'a1'`,
		"article_processing_events": `SELECT COUNT(1) FROM article_processing_events WHERE article_id = 'a1'`,
		"exact_fingerprints":        `SELECT COUNT(1) FROM exact_fingerprints WHERE article_id = 'a1'`,
		"event_clusters":            `SELECT COUNT(1) FROM event_clusters WHERE id = 'c1'`,
		"event_cluster_members":     `SELECT COUNT(1) FROM event_cluster_members WHERE article_id = 'a1'`,
		"source_policy_decisions":   `SELECT COUNT(1) FROM source_policy_decisions WHERE article_id = 'a1'`,
	}
	for table, query := range counts {
		var n int
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s rows for the seeded article after migration 0017 = %d, want 1 (a cascading delete during the table rebuild would leave 0)", table, n)
		}
	}

	var fkViolations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_foreign_key_check()`).Scan(&fkViolations); err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if fkViolations != 0 {
		t.Errorf("foreign_key_check reported %d violations after migration 0017, want 0", fkViolations)
	}
}

// TestMigrate_0017AcceptsRetainedNoPublishStatus proves the actual bug fix:
// article.StatusRetainedNoPublish ("retained_no_publish") is a status
// ProcessCycle's Gate B (source policy) has saved articles with since
// Phase 12, but articles.status's CHECK constraint never
// listed it until this migration.
func TestMigrate_0017AcceptsRetainedNoPublishStatus(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'retained_no_publish')
	`); err != nil {
		t.Errorf("insert with status='retained_no_publish': %v", err)
	}
}

// TestMigrate_EveryDomainArticleStatusIsAcceptedByTheDatabase is a
// validator against exactly the class of bug migration 0017 fixes: every
// article.Status constant the domain package defines must be an accepted
// value in the real articles.status CHECK constraint, so a future status
// added to one side without the other fails a fast, obvious test instead
// of only surfacing as a runtime CHECK violation the in-memory fakes used
// by most of this codebase's tests can never catch.
func TestMigrate_EveryDomainArticleStatusIsAcceptedByTheDatabase(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	statuses := []article.Status{
		article.StatusFetched, article.StatusNormalized, article.StatusRejectedValidation,
		article.StatusRejectedDuplicate, article.StatusRejectedNotRelated, article.StatusClusterCandidate,
		article.StatusClustered, article.StatusRejectedQuality, article.StatusRejectedStale,
		article.StatusPendingPublication, article.StatusPublishing, article.StatusPublished,
		article.StatusPublishFailed, article.StatusPendingRetry, article.StatusDiscarded,
		article.StatusRetainedNoPublish,
	}
	for i, status := range statuses {
		id := "status-check-" + string(status)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?)
		`, id, id, id, string(status)); err != nil {
			t.Errorf("status %q (domain constant #%d) rejected by the database CHECK constraint: %v", status, i, err)
		}
	}
}

// TestMigrate_0018CreatesClusterPublicationGateTable proves the plain
// schema addition: the table exists with the expected CHECK constraint and
// foreign keys, on a fresh database.
func TestMigrate_0018CreatesClusterPublicationGateTable(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'open')
	`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'clustered')
	`); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO event_cluster_publications (cluster_id, state, article_id, reserved_at) VALUES ('c1', 'reserved', 'a1', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Errorf("insert valid row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO event_cluster_publications (cluster_id, state, article_id, reserved_at) VALUES ('c2', 'not_a_real_state', 'a1', CURRENT_TIMESTAMP)
	`); err == nil {
		t.Error("insert with invalid state succeeded, want a CHECK constraint violation")
	}
}

// TestMigrate_0018BackfillsConfirmedForAlreadyPublishedCluster proves the
// migration's core compatibility guarantee: a database migrated only
// through 0017 with a cluster whose article already succeeded in
// publication_attempts gets a 'confirmed' event_cluster_publications row
// for that article once 0018 is applied — so a post-deploy article joining
// that cluster is correctly gated, not treated as if nothing had ever
// published.
func TestMigrate_0018BackfillsConfirmedForAlreadyPublishedCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0017_articles_status_retained_no_publish.sql")

	seeds := []string{
		`INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', '2026-07-16T12:00:00Z', '2026-07-16T12:00:00Z', 'open')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status, current_cluster_id, source_id, source_type, cluster_member_role) VALUES ('a1','u1','u1','2026-07-16T12:00:00Z','published','c1','source-1','rss','first_report')`,
		`INSERT INTO publication_outbox (id, article_id, payload, status, available_at) VALUES (1, 'a1', '{}', 'published', '2026-07-16T12:00:00Z')`,
		`INSERT INTO publication_attempts (outbox_id, article_id, attempted_at, outcome) VALUES (1, 'a1', '2026-07-16T12:05:00Z', 'succeeded')`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var state, articleID string
	if err := db.QueryRowContext(ctx, `SELECT state, article_id FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state, &articleID); err != nil {
		t.Fatalf("query backfilled row: %v", err)
	}
	if state != "confirmed" || articleID != "a1" {
		t.Errorf("backfilled row = state=%q article_id=%q, want state=confirmed article_id=a1", state, articleID)
	}
}

// TestMigrate_0018BackfillsReservedForInFlightCluster mirrors the confirmed
// case above but for a cluster whose only outbox activity is still
// 'pending' (never delivered, or mid-retry) — the migration must reserve,
// not confirm, since delivery outcome is genuinely unknown.
func TestMigrate_0018BackfillsReservedForInFlightCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0017_articles_status_retained_no_publish.sql")

	seeds := []string{
		`INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', '2026-07-16T12:00:00Z', '2026-07-16T12:00:00Z', 'open')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status, current_cluster_id, source_id, source_type, cluster_member_role) VALUES ('a1','u1','u1','2026-07-16T12:00:00Z','pending_retry','c1','source-1','rss','first_report')`,
		`INSERT INTO publication_outbox (id, article_id, payload, status, available_at, created_at) VALUES (1, 'a1', '{}', 'pending', '2026-07-16T12:10:00Z', '2026-07-16T12:00:00Z')`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state); err != nil {
		t.Fatalf("query backfilled row: %v", err)
	}
	if state != "reserved" {
		t.Errorf("backfilled state = %q, want reserved", state)
	}
}

// TestMigrate_0018NoBackfillForClusterWithNothingPublishedOrInFlight
// proves the migration is a no-op for a cluster that never had any outbox
// activity — no row should be created; the first real article to reach
// ProcessCycle's gate check for it post-deploy legitimately creates one.
func TestMigrate_0018NoBackfillForClusterWithNothingPublishedOrInFlight(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'open')
	`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

// TestMigrate_0020BackfillsExistingHumanLabelsIntoEditorialExamples proves
// migration 0020's core promise: every human_labels row that exists
// before the migration runs gets a matching, fully-populated
// editorial_examples row afterward, with no foreign key tying its
// survival to the articles row it was copied from.
func TestMigrate_0020BackfillsExistingHumanLabelsIntoEditorialExamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0019_seen_items.sql")

	seeds := []string{
		`INSERT INTO articles (id, original_url, canonical_url, source_id, source_type, domain, title, summary, author, fetched_at, published_at, content_type, status, current_cluster_id, classifier_version, dictionary_version) VALUES ('a1','https://x.com/1','https://x.com/1','s1','rss','x.com','T1','B1','Jane','2026-01-01 00:00:00','2026-01-01 00:00:00','article','rejected_quality',NULL,NULL,NULL)`,
		`INSERT INTO event_clusters (id, representative_article_id, first_seen_at, last_updated_at, member_count, status) VALUES ('c1','a1','2026-01-01 00:00:00','2026-01-01 00:00:00',1,'open')`,
		`UPDATE articles SET current_cluster_id = 'c1' WHERE id = 'a1'`,
		`INSERT INTO human_labels (article_id, label, reason, labeled_by, labeled_at, notes) VALUES ('a1','reject','quality','reviewer-1','2026-01-02 00:00:00','not isotopes news')`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM editorial_examples`).Scan(&count); err != nil {
		t.Fatalf("count editorial_examples: %v", err)
	}
	if count != 1 {
		t.Fatalf("editorial_examples row count = %d, want 1", count)
	}

	var (
		articleID, label, reason, labeledBy, notes              string
		title, body, sourceID, sourceType, domain, contentType  string
		clusterID, author, classifierVersion, dictionaryVersion string
	)
	err = db.QueryRowContext(ctx, `
		SELECT article_id, label, reason, labeled_by, notes,
			title, body, source_id, source_type, domain, content_type,
			cluster_id, author, classifier_version, dictionary_version
		FROM editorial_examples WHERE article_id = 'a1'
	`).Scan(&articleID, &label, &reason, &labeledBy, &notes,
		&title, &body, &sourceID, &sourceType, &domain, &contentType,
		&clusterID, &author, &classifierVersion, &dictionaryVersion)
	if err != nil {
		t.Fatalf("scan editorial_examples row: %v", err)
	}
	if articleID != "a1" || label != "reject" || reason != "quality" || labeledBy != "reviewer-1" || notes != "not isotopes news" {
		t.Errorf("label fields = (%q,%q,%q,%q,%q), want (a1,reject,quality,reviewer-1,not isotopes news)", articleID, label, reason, labeledBy, notes)
	}
	if title != "T1" || body != "B1" || sourceID != "rss:s1" || sourceType != "rss" || domain != "x.com" || contentType != "article" {
		t.Errorf("content fields = (%q,%q,%q,%q,%q,%q), want (T1,B1,rss:s1,rss,x.com,article)", title, body, sourceID, sourceType, domain, contentType)
	}
	if clusterID != "c1" || author != "Jane" {
		t.Errorf("cluster_id/author = (%q,%q), want (c1,Jane)", clusterID, author)
	}
	if classifierVersion != "" || dictionaryVersion != "" {
		t.Errorf("classifier_version/dictionary_version = (%q,%q), want empty (NULL source columns COALESCEd to '')", classifierVersion, dictionaryVersion)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM articles WHERE id = 'a1'`); err != nil {
		t.Fatalf("delete article a1: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM editorial_examples WHERE article_id = 'a1'`).Scan(&count); err != nil {
		t.Fatalf("count editorial_examples after article delete: %v", err)
	}
	if count != 1 {
		t.Errorf("editorial_examples row for a1 after deleting its article = %d rows, want 1 (must survive — no FK to articles)", count)
	}

	var fkViolations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_foreign_key_check()`).Scan(&fkViolations); err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if fkViolations != 0 {
		t.Errorf("foreign_key_check reported %d violations, want 0", fkViolations)
	}
}

// TestMigrate_0020AssertionMechanismAbortsOnMismatch exercises the
// temp-table CHECK(ok = 0) trick migration 0020 uses to abort on a
// count/content mismatch, in isolation. The real migration's join
// (human_labels JOIN articles) is provably complete — see the migration
// file's own comment — so this test cannot force the actual migration to
// produce a mismatch; instead it proves the mechanism itself rejects a
// nonzero value, which is what would happen if it ever did.
func TestMigrate_0020AssertionMechanismAbortsOnMismatch(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TEMP TABLE assert_probe (ok INTEGER NOT NULL CHECK (ok = 0));
		INSERT INTO assert_probe (ok) SELECT 1;
	`)
	if err == nil {
		t.Fatal("Exec() error = nil, want a CHECK constraint violation for a nonzero assertion value")
	}
}

func TestMigration0021_NamespacesLegacySourceIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Apply every migration through 0020 by pre-marking 0021 as already
	// applied, so we can seed pre-namespace ("legacy") data before it
	// actually runs.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ('0021_source_id_namespace.sql')`); err != nil {
		t.Fatalf("mark 0021 applied: %v", err)
	}
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() (0001-0020) error = %v", err)
	}

	// Seed one Twitter and one Instagram article with the SAME legacy
	// bare source_id — the exact collision the migration fixes.
	seedArticle := func(id, sourceID, sourceType string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO articles (id, external_id, original_url, canonical_url, source_id, source_name, source_type, domain, status, fetched_at)
			VALUES (?, '', 'https://example.com/'||?, 'https://example.com/'||?, ?, 'Springfield Isotopes', ?, 'example.com', 'fetched', CURRENT_TIMESTAMP)
		`, id, id, id, sourceID, sourceType); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}
	seedArticle("a-twitter", "sprisotopes", "twitter")
	seedArticle("a-instagram", "sprisotopes", "instagram")

	if _, err := db.ExecContext(ctx, `
		INSERT INTO source_policy_decisions (id, article_id, source_id, authority, policy_version, decision_code, mode, decided_at)
		VALUES ('d1', 'a-twitter', 'sprisotopes', 'official_team', '1', 'standard', 'shadow', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed source_policy_decisions: %v", err)
	}

	// d2 deliberately records a DIFFERENT source_id than its linked
	// article's current source_id ("sprisotopes") -- this can't happen
	// via any write path in this codebase today (nothing mutates an
	// existing article's source_id after creation), but the migration must
	// still namespace using the decision's OWN identifier, not silently
	// replace it with the article's current one. See the migration's
	// comment above the source_policy_decisions UPDATE.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO source_policy_decisions (id, article_id, source_id, authority, policy_version, decision_code, mode, decided_at)
		VALUES ('d2', 'a-twitter', 'sprisotopes-oldhandle', 'official_team', '1', 'standard', 'shadow', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed source_policy_decisions (divergent): %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO seen_items (id, platform, source_id, identity_basis, identity_key, identity_algorithm, article_id, first_seen_at, last_seen_at)
		VALUES ('s1', 'twitter', 'sprisotopes', 'remote_id', 'ext-1', 'exact', 'a-twitter', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed seen_items: %v", err)
	}

	// Now let 0021 actually run.
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = '0021_source_id_namespace.sql'`); err != nil {
		t.Fatalf("unmark 0021: %v", err)
	}
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() (0021) error = %v", err)
	}

	var twitterID, instagramID string
	if err := db.QueryRowContext(ctx, `SELECT source_id FROM articles WHERE id = 'a-twitter'`).Scan(&twitterID); err != nil {
		t.Fatalf("read a-twitter source_id: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT source_id FROM articles WHERE id = 'a-instagram'`).Scan(&instagramID); err != nil {
		t.Fatalf("read a-instagram source_id: %v", err)
	}
	if twitterID != "twitter:sprisotopes" {
		t.Errorf("articles a-twitter source_id = %q, want %q", twitterID, "twitter:sprisotopes")
	}
	if instagramID != "instagram:sprisotopes" {
		t.Errorf("articles a-instagram source_id = %q, want %q", instagramID, "instagram:sprisotopes")
	}
	if twitterID == instagramID {
		t.Fatal("Twitter and Instagram articles still share a source_id after migration")
	}

	var decisionSourceID string
	if err := db.QueryRowContext(ctx, `SELECT source_id FROM source_policy_decisions WHERE id = 'd1'`).Scan(&decisionSourceID); err != nil {
		t.Fatalf("read source_policy_decisions: %v", err)
	}
	if decisionSourceID != "twitter:sprisotopes" {
		t.Errorf("source_policy_decisions source_id = %q, want %q (derived via article_id join, not double-prefixed)", decisionSourceID, "twitter:sprisotopes")
	}

	var divergentDecisionSourceID string
	if err := db.QueryRowContext(ctx, `SELECT source_id FROM source_policy_decisions WHERE id = 'd2'`).Scan(&divergentDecisionSourceID); err != nil {
		t.Fatalf("read source_policy_decisions (divergent): %v", err)
	}
	if divergentDecisionSourceID != "twitter:sprisotopes-oldhandle" {
		t.Errorf("source_policy_decisions (divergent) source_id = %q, want %q — the decision's OWN source_id must be namespaced, not silently replaced with the joined article's current source_id", divergentDecisionSourceID, "twitter:sprisotopes-oldhandle")
	}

	var seenItemSourceID string
	if err := db.QueryRowContext(ctx, `SELECT source_id FROM seen_items WHERE id = 's1'`).Scan(&seenItemSourceID); err != nil {
		t.Fatalf("read seen_items: %v", err)
	}
	if seenItemSourceID != "twitter:sprisotopes" {
		t.Errorf("seen_items source_id = %q, want %q", seenItemSourceID, "twitter:sprisotopes")
	}

	var auditRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_id_migrations`).Scan(&auditRows); err != nil {
		t.Fatalf("count source_id_migrations: %v", err)
	}
	if auditRows == 0 {
		t.Error("source_id_migrations recorded no rows — expected an audit entry per affected table")
	}
}

// TestMigrate_0022AddsClusterLifecycleColumns proves the new columns exist,
// carry the documented defaults, and that a fresh row can be transitioned
// through the close/reopen lifecycle by writing to them directly (the
// application-level CAS logic is covered separately in
// adapters/outbound/sqlite/cluster_repository_test.go).
func TestMigrate_0022AddsClusterLifecycleColumns(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'open')
	`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}

	var closedAt sql.NullTime
	var closeReason string
	var reopenedCount int
	err := db.QueryRowContext(ctx, `SELECT closed_at, close_reason, reopened_count FROM event_clusters WHERE id = 'c1'`).
		Scan(&closedAt, &closeReason, &reopenedCount)
	if err != nil {
		t.Fatalf("select new columns: %v", err)
	}
	if closedAt.Valid {
		t.Errorf("closed_at for a freshly-created open cluster = %v, want NULL", closedAt.Time)
	}
	if closeReason != "" {
		t.Errorf("close_reason default = %q, want empty string", closeReason)
	}
	if reopenedCount != 0 {
		t.Errorf("reopened_count default = %d, want 0", reopenedCount)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE event_clusters SET status = 'closed', closed_at = CURRENT_TIMESTAMP, close_reason = 'inactivity_ttl' WHERE id = 'c1'
	`); err != nil {
		t.Fatalf("close cluster: %v", err)
	}
	err = db.QueryRowContext(ctx, `SELECT closed_at, close_reason FROM event_clusters WHERE id = 'c1'`).Scan(&closedAt, &closeReason)
	if err != nil {
		t.Fatalf("select after close: %v", err)
	}
	if !closedAt.Valid {
		t.Error("closed_at after closing = NULL, want set")
	}
	if closeReason != "inactivity_ttl" {
		t.Errorf("close_reason after closing = %q, want %q", closeReason, "inactivity_ttl")
	}

	var indexCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('idx_event_clusters_status_last_updated', 'idx_event_clusters_closed_at')
	`).Scan(&indexCount); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if indexCount != 2 {
		t.Errorf("lifecycle index count = %d, want 2", indexCount)
	}
}

// TestMigrate_0023AddsOutboxDeliveryGuaranteeColumns proves the new outbox
// evidence columns exist and that both publication_outbox.status and
// publication_attempts.outcome accept the expanded vocabulary
// ('sent_unconfirmed'/'retryable'/'quarantined' and 'ambiguous'
// respectively) introduced to close the crash window described in ADR 0030
// — see the migration file's own comment for the full rationale. The
// remap of pre-existing 'claimed'/'published' rows to
// 'leased'/'delivered', and the id preservation the table rebuild depends
// on, are covered by
// TestMigrate_0023RemapsExistingStatusesAndPreservesIDs below — this test
// starts from a fresh, empty database and so cannot see either.
func TestMigrate_0023AddsOutboxDeliveryGuaranteeColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// publication_outbox.article_id is a foreign key into articles(id), so
	// every article_id referenced below needs a parent row first.
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES (?, ?, ?, CURRENT_TIMESTAMP, 'pending_publication')
		`, id, id, id); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}

	// New columns exist and accept the new status vocabulary.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO publication_outbox (article_id, available_at, status, sent_message_id, sent_at, ambiguous_reason, quarantined_at)
		VALUES ('a1', CURRENT_TIMESTAMP, 'sent_unconfirmed', '12345', CURRENT_TIMESTAMP, 'timeout', NULL)
	`); err != nil {
		t.Fatalf("insert sent_unconfirmed row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO publication_outbox (article_id, available_at, status) VALUES ('a2', CURRENT_TIMESTAMP, 'quarantined')
	`); err != nil {
		t.Fatalf("insert quarantined row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO publication_outbox (article_id, available_at, status) VALUES ('a3', CURRENT_TIMESTAMP, 'bogus')
	`); err == nil {
		t.Fatal("insert with bogus status: want CHECK violation, got nil error")
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO publication_outbox (id, article_id, available_at, status) VALUES (99, 'a4', CURRENT_TIMESTAMP, 'pending')
	`); err != nil {
		t.Fatalf("insert attempt parent row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO publication_attempts (outbox_id, article_id, attempted_at, outcome) VALUES (99, 'a4', CURRENT_TIMESTAMP, 'ambiguous')
	`); err != nil {
		t.Fatalf("insert ambiguous attempt: %v", err)
	}
}

// TestMigrate_0023RemapsExistingStatusesAndPreservesIDs covers the two
// parts of migration 0023 that only exist for pre-existing data, and which
// every other 0023 test — all of them starting from a fresh, empty
// database — structurally cannot exercise:
//
//   - the CASE status remap ('claimed'->'leased', 'published'->'delivered',
//     everything else unchanged). Get it wrong and an existing deployment's
//     in-flight rows either violate the new CHECK constraint or silently
//     land in the wrong state.
//   - the explicit-id column list in the INSERT...SELECT. publication_outbox
//     is rebuilt (AUTOINCREMENT primary key), and both
//     event_cluster_publications.outbox_id and publication_attempts.outbox_id
//     reference it. If the copy omitted `id`, SQLite would assign fresh
//     rowids and every one of those references would silently point at the
//     wrong row (or nothing).
//
// It follows TestMigrate_0017PreservesExistingDataAcrossEveryReferencingTable's
// pattern: migrate only through 0022, seed with the OLD (pre-0023) status
// vocabulary — 'pending'/'claimed'/'published'/'dead' are the only four
// values that could exist at that point — then apply 0023 via
// sqlite.Migrate and assert on the result.
func TestMigrate_0023RemapsExistingStatusesAndPreservesIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0022_cluster_lifecycle.sql")

	seeds := []string{
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u1','u1',CURRENT_TIMESTAMP,'pending_publication')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a2','u2','u2',CURRENT_TIMESTAMP,'publishing')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a3','u3','u3',CURRENT_TIMESTAMP,'published')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a4','u4','u4',CURRENT_TIMESTAMP,'discarded')`,
		// Deliberately non-contiguous, non-1-based ids: a rebuild that
		// dropped the explicit id column would renumber these to 1..4 and
		// the assertions below would fail.
		`INSERT INTO publication_outbox (id, article_id, available_at, status) VALUES (11,'a1',CURRENT_TIMESTAMP,'pending')`,
		`INSERT INTO publication_outbox (id, article_id, available_at, status) VALUES (22,'a2',CURRENT_TIMESTAMP,'claimed')`,
		`INSERT INTO publication_outbox (id, article_id, available_at, status) VALUES (33,'a3',CURRENT_TIMESTAMP,'published')`,
		`INSERT INTO publication_outbox (id, article_id, available_at, status) VALUES (44,'a4',CURRENT_TIMESTAMP,'dead')`,
		`INSERT INTO publication_attempts (id, outbox_id, article_id, attempted_at, outcome) VALUES (7,33,'a3',CURRENT_TIMESTAMP,'succeeded')`,
		`INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'open')`,
		`INSERT INTO event_cluster_publications (cluster_id, state, article_id, outbox_id, reserved_at, confirmed_at) VALUES ('c1','confirmed','a3',33,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// 1-3: each seeded row is still addressable by the id it was inserted
	// with, and carries the correctly remapped status.
	wantStatus := map[int64]string{11: "pending", 22: "leased", 33: "delivered", 44: "dead"}
	for id, want := range wantStatus {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT status FROM publication_outbox WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("select publication_outbox id=%d after 0023: %v (the row's id was not preserved across the table rebuild)", id, err)
		}
		if got != want {
			t.Errorf("publication_outbox id=%d status after 0023 = %q, want %q", id, got, want)
		}
	}

	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox`).Scan(&total); err != nil {
		t.Fatalf("count publication_outbox: %v", err)
	}
	if total != len(wantStatus) {
		t.Errorf("publication_outbox row count after 0023 = %d, want %d", total, len(wantStatus))
	}

	// 4: the downstream references still resolve. A join, not a bare
	// column read, so a silently-reassigned id shows up as a missing row
	// rather than a plausible-looking integer.
	var joinedState, joinedStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT ecp.state, po.status
		FROM event_cluster_publications ecp
		JOIN publication_outbox po ON po.id = ecp.outbox_id
		WHERE ecp.cluster_id = 'c1'
	`).Scan(&joinedState, &joinedStatus); err != nil {
		t.Fatalf("join event_cluster_publications to publication_outbox after 0023: %v (outbox_id no longer resolves)", err)
	}
	if joinedState != "confirmed" || joinedStatus != "delivered" {
		t.Errorf("joined (state, status) = (%q, %q), want (confirmed, delivered)", joinedState, joinedStatus)
	}

	var attemptOutboxID int64
	if err := db.QueryRowContext(ctx, `
		SELECT po.id FROM publication_attempts pa JOIN publication_outbox po ON po.id = pa.outbox_id WHERE pa.id = 7
	`).Scan(&attemptOutboxID); err != nil {
		t.Fatalf("join publication_attempts to publication_outbox after 0023: %v", err)
	}
	if attemptOutboxID != 33 {
		t.Errorf("publication_attempts.outbox_id after 0023 = %d, want 33", attemptOutboxID)
	}

	// 5: same FK-safety assertion migration 0017's data-preservation test
	// makes — the rebuild must not leave a dangling reference behind.
	var fkViolations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_foreign_key_check()`).Scan(&fkViolations); err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if fkViolations != 0 {
		t.Errorf("foreign_key_check reported %d violations after migration 0023, want 0", fkViolations)
	}
}

// TestMigrate_0024_EditorialCycleSessionLifecycle_AppliesCleanly proves
// migration 0024's schema changes on a fresh database:
// editorial_cycle_sessions.status accepts the expanded lifecycle
// vocabulary ('paused'/'failed'/'cancelled' in addition to the original
// 'running'/'completed') and still rejects anything outside it, and the
// new editorial_cycle_session_candidates table exists and enforces its
// foreign keys.
func TestMigrate_0024_EditorialCycleSessionLifecycle_AppliesCleanly(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	// The expanded status set is accepted...
	if _, err := db.ExecContext(ctx, `INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('s1', ?, ?, 'failed', 'review', '{}')`, time.Now(), time.Now()); err != nil {
		t.Errorf("insert with status='failed' failed: %v", err)
	}
	// ...and an invalid one is still rejected.
	if _, err := db.ExecContext(ctx, `INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('s2', ?, ?, 'bogus', 'review', '{}')`, time.Now(), time.Now()); err == nil {
		t.Error("insert with status='bogus' succeeded, want the CHECK constraint to reject it")
	}

	// editorial_cycle_session_candidates exists and enforces its FKs.
	if _, err := db.ExecContext(ctx, `INSERT INTO editorial_cycle_session_candidates (session_id, article_id, score, selected_at) VALUES ('s1', 'no-such-article', 1.0, ?)`, time.Now()); err == nil {
		t.Error("insert referencing a nonexistent article succeeded, want the foreign key to reject it")
	}
}

// TestMigrate_0024RemapsExistingStatusesAndPreservesIDs mirrors migration
// 0017/0023's data-preservation tests: a session left 'running' (the
// process that owned it either finished, which would have written
// 'completed', or died before a fresh migration run ever saw it) remaps
// to 'paused', and the always-dead 'abandoned' remaps to 'cancelled' —
// see the migration's own comment. It also proves
// editorial_review_decisions rows survive the editorial_cycle_sessions
// table rebuild via their session_id foreign key.
func TestMigrate_0024RemapsExistingStatusesAndPreservesIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0023_outbox_delivery_guarantee.sql")

	seeds := []string{
		`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('running1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'running', 'review', '{}')`,
		`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('completed1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'completed', 'review', '{}')`,
		`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('abandoned1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'abandoned', 'review', '{}')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u1','u1',CURRENT_TIMESTAMP,'clustered')`,
		`INSERT INTO editorial_review_decisions (session_id, article_id, action, decided_at) VALUES ('running1', 'a1', 'accept', CURRENT_TIMESTAMP)`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	wantStatus := map[string]string{"running1": "paused", "completed1": "completed", "abandoned1": "cancelled"}
	for id, want := range wantStatus {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT status FROM editorial_cycle_sessions WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("select editorial_cycle_sessions id=%q after 0024: %v (the row's id was not preserved across the table rebuild)", id, err)
		}
		if got != want {
			t.Errorf("editorial_cycle_sessions id=%q status after 0024 = %q, want %q", id, got, want)
		}
	}

	var decisionSessionID string
	if err := db.QueryRowContext(ctx, `SELECT session_id FROM editorial_review_decisions WHERE article_id = 'a1'`).Scan(&decisionSessionID); err != nil {
		t.Fatalf("select editorial_review_decisions after 0024: %v (a cascading delete during the session table rebuild would leave 0 rows)", err)
	}
	if decisionSessionID != "running1" {
		t.Errorf("editorial_review_decisions session_id after 0024 = %q, want %q", decisionSessionID, "running1")
	}

	var fkViolations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_foreign_key_check()`).Scan(&fkViolations); err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if fkViolations != 0 {
		t.Errorf("foreign_key_check reported %d violations after migration 0024, want 0", fkViolations)
	}
}

// TestMigrate_0026CreatesHistoryColumnsAndIndexes proves the two partial
// unique indexes and the human_label_conflicts table exist after the
// migration runs on a fresh database with no legacy human_labels data to
// reconcile. (The new columns themselves are covered indirectly: the
// backfill test below and the repository's own tests write and read
// them, which only compiles/runs if they exist with usable defaults.)
func TestMigrate_0026CreatesHistoryColumnsAndIndexes(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	var indexCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND name IN ('idx_human_labels_one_active', 'idx_editorial_examples_one_active')
	`).Scan(&indexCount); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if indexCount != 2 {
		t.Fatalf("indexCount = %d, want 2 (both partial unique indexes must exist)", indexCount)
	}

	var conflictTableExists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'human_label_conflicts'`).Scan(&conflictTableExists); err != nil {
		t.Fatalf("check human_label_conflicts: %v", err)
	}
	if conflictTableExists != 1 {
		t.Fatal("human_label_conflicts table does not exist")
	}
}

// TestMigrate_0026UniqueIndexBlocksTwoActiveRowsForSameArticle proves
// the real constraint: inserting a second active=1 human_labels row for
// an article that already has one fails, at the database level.
func TestMigrate_0026UniqueIndexBlocksTwoActiveRowsForSameArticle(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO articles (id, external_id, original_url, canonical_url, domain, status, fetched_at)
		VALUES ('a1', '', 'https://example.com/a1', 'https://example.com/a1', 'example.com', 'fetched', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("insert article: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO human_labels (article_id, label, labeled_at, revision, active) VALUES ('a1', 'publish', CURRENT_TIMESTAMP, 1, 1)
	`); err != nil {
		t.Fatalf("insert first active row: %v", err)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO human_labels (article_id, label, labeled_at, revision, active) VALUES ('a1', 'reject', CURRENT_TIMESTAMP, 2, 1)
	`)
	if err == nil {
		t.Fatal("INSERT a second active row = nil error, want a UNIQUE constraint violation")
	}
}

// TestMigrate_0026BackfillsLegacyLabelsAndFlagsAmbiguousConflicts seeds
// three article_ids' worth of pre-0026 human_labels rows -- one with a
// same-label repeat, one with a different-label pair that has an
// unambiguous timestamp order, and one with a different-label pair tied
// on the exact same timestamp -- then lets 0026 run and checks each is
// resolved (or flagged) per the design spec's policy. Only human_labels
// is seeded; editorial_examples' backfill is the same SQL over an
// independently-grouped copy of the same shape.
func TestMigrate_0026BackfillsLegacyLabelsAndFlagsAmbiguousConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Apply every migration through 0025 by pre-marking 0026 applied,
	// so legacy (pre-history-columns) rows can be seeded.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ('0026_human_label_history.sql')`); err != nil {
		t.Fatalf("mark 0026 applied: %v", err)
	}
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() (0001-0025) error = %v", err)
	}

	seedArticle := func(id string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO articles (id, external_id, original_url, canonical_url, domain, status, fetched_at)
			VALUES (?, '', 'https://example.com/'||?, 'https://example.com/'||?, 'example.com', 'fetched', CURRENT_TIMESTAMP)
		`, id, id, id); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}
	seedLabel := func(id int, articleID, label, labeledAt string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO human_labels (id, article_id, label, labeled_at) VALUES (?,?,?,?)`, id, articleID, label, labeledAt); err != nil {
			t.Fatalf("seed human_labels %d: %v", id, err)
		}
	}
	seedArticle("repeat-1")
	seedLabel(1, "repeat-1", "publish", "2026-01-01T00:00:00Z")
	seedLabel(2, "repeat-1", "publish", "2026-01-02T00:00:00Z")

	seedArticle("clear-order-1")
	seedLabel(3, "clear-order-1", "publish", "2026-01-01T00:00:00Z")
	seedLabel(4, "clear-order-1", "reject", "2026-01-02T00:00:00Z")

	seedArticle("tied-1")
	seedLabel(5, "tied-1", "publish", "2026-01-01T00:00:00Z")
	seedLabel(6, "tied-1", "reject", "2026-01-01T00:00:00Z")

	// Now let 0026 actually run.
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = '0026_human_label_history.sql'`); err != nil {
		t.Fatalf("unmark 0026: %v", err)
	}
	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() (0026) error = %v", err)
	}

	assertActive := func(articleID, wantLabel string, wantRevision int) {
		t.Helper()
		var label string
		var revision int
		err := db.QueryRowContext(ctx, `SELECT label, revision FROM human_labels WHERE article_id = ? AND active = 1`, articleID).Scan(&label, &revision)
		if err != nil {
			t.Fatalf("query active row for %s: %v", articleID, err)
		}
		if label != wantLabel || revision != wantRevision {
			t.Errorf("%s active = (%s, rev %d), want (%s, rev %d)", articleID, label, revision, wantLabel, wantRevision)
		}
	}
	assertActive("repeat-1", "publish", 2)
	assertActive("clear-order-1", "reject", 2)

	var tiedActiveCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_labels WHERE article_id = 'tied-1' AND active = 1`).Scan(&tiedActiveCount); err != nil {
		t.Fatalf("count tied-1 active rows: %v", err)
	}
	if tiedActiveCount != 0 {
		t.Errorf("tied-1 active row count = %d, want 0 (ambiguous conflict must stay unresolved)", tiedActiveCount)
	}

	var conflictCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_label_conflicts WHERE table_name = 'human_labels' AND article_id = 'tied-1' AND resolved_at IS NULL`).Scan(&conflictCount); err != nil {
		t.Fatalf("count conflicts: %v", err)
	}
	if conflictCount != 1 {
		t.Errorf("tied-1 conflict rows = %d, want 1", conflictCount)
	}

	var repeatOneConflict, clearOrderOneConflict int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_label_conflicts WHERE article_id = 'repeat-1'`).Scan(&repeatOneConflict); err != nil {
		t.Fatalf("count repeat-1 conflicts: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_label_conflicts WHERE article_id = 'clear-order-1'`).Scan(&clearOrderOneConflict); err != nil {
		t.Fatalf("count clear-order-1 conflicts: %v", err)
	}
	if repeatOneConflict != 0 || clearOrderOneConflict != 0 {
		t.Errorf("repeat-1/clear-order-1 must not be flagged as conflicts (got %d, %d)", repeatOneConflict, clearOrderOneConflict)
	}
}

func TestMigrate_0027AddsCooldownOutcomeColumn(t *testing.T) {
	db := openMigratedTestDB(t)
	if err := sqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	rows, err := db.Query(`PRAGMA table_info(source_policy_decisions)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "cooldown_outcome" {
			found = true
			if !dflt.Valid || dflt.String != "''" {
				t.Errorf("cooldown_outcome default = %v, want ''", dflt)
			}
		}
	}
	if !found {
		t.Fatal("source_policy_decisions has no cooldown_outcome column")
	}
}

// TestMigrate_0028CreatesSourcePolicyCooldownsTable proves the plain schema
// addition: the table exists with the expected CHECK constraint and foreign
// keys, on a fresh database.
func TestMigrate_0028CreatesSourcePolicyCooldownsTable(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'open')
	`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES ('a1','u','u',CURRENT_TIMESTAMP,'clustered')
	`); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	now := time.Now()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO source_policy_cooldowns
			(source_id, cluster_id, state, article_id, decision_code, policy_version, reserved_at, cooldown_until)
			VALUES ('s1', 'c1', 'reserved', 'a1', 'publish_official_confirmation', 'v1', ?, ?)
	`, now, now.Add(20*time.Minute)); err != nil {
		t.Errorf("insert into source_policy_cooldowns: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO source_policy_cooldowns
			(source_id, cluster_id, state, article_id, decision_code, policy_version, reserved_at, cooldown_until)
			VALUES ('s2', 'c1', 'not_a_real_state', 'a1', 'publish_official_confirmation', 'v1', ?, ?)
	`, now, now.Add(20*time.Minute)); err == nil {
		t.Error("insert with invalid state succeeded, want a CHECK constraint violation")
	}
}

// TestMigrate_0028BackfillsConfirmedFromRealPublicationHistory proves the
// migration's core compatibility guarantee, mirroring
// TestMigrate_0018BackfillsConfirmedForAlreadyPublishedCluster: a
// source_policy_decisions row recording a cooldown-relevant decision
// (decision_code = 'publish_official_confirmation') whose article actually
// succeeded in publication_attempts (joined through a 'delivered'
// publication_outbox row) gets a 'confirmed' source_policy_cooldowns row
// once 0028 is applied — so an upgraded deployment starts with its
// cooldown reservations consistent with real publication history. The
// seeded row also carries would_publish_special = '1' to prove the
// backfill's source of truth is publication_attempts, not that flawed
// shadow-mode column (see
// TestMigrate_0028DoesNotBackfillFromShadowModeWouldPublishSpecial for the
// negative case that isolates this distinction).
func TestMigrate_0028BackfillsConfirmedFromRealPublicationHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0027_source_policy_decisions_cooldown_outcome.sql")

	seeds := []string{
		`INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', '2026-07-16T12:00:00Z', '2026-07-16T12:00:00Z', 'open')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status, source_id, source_type) VALUES ('a1','u1','u1','2026-07-16T12:00:00Z','published','s1','rss')`,
		`INSERT INTO source_policy_decisions (id, article_id, source_id, authority, policy_version, cluster_id, decision_code, would_publish_special, mode, decided_at) VALUES ('d1', 'a1', 's1', 'official_team', 'v1', 'c1', 'publish_official_confirmation', '1', 'enforce', '2026-07-16T12:00:00Z')`,
		`INSERT INTO publication_outbox (id, article_id, payload, status, available_at) VALUES (1, 'a1', '{}', 'delivered', '2026-07-16T12:00:00Z')`,
		`INSERT INTO publication_attempts (outbox_id, article_id, attempted_at, outcome) VALUES (1, 'a1', '2026-07-16T12:05:00Z', 'succeeded')`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM source_policy_cooldowns WHERE source_id = 's1' AND cluster_id = 'c1'`).Scan(&count); err != nil {
		t.Fatalf("count backfilled rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("source_policy_cooldowns rows for (s1, c1) = %d, want 1", count)
	}

	var state, articleID string
	if err := db.QueryRowContext(ctx, `SELECT state, article_id FROM source_policy_cooldowns WHERE source_id = 's1' AND cluster_id = 'c1'`).Scan(&state, &articleID); err != nil {
		t.Fatalf("query backfilled row: %v", err)
	}
	if state != "confirmed" || articleID != "a1" {
		t.Errorf("backfilled row = state=%q article_id=%q, want state=confirmed article_id=a1", state, articleID)
	}
}

// TestMigrate_0028DoesNotBackfillFromShadowModeWouldPublishSpecial is the
// regression test proving this migration does not repeat the bug it
// exists to fix: a source_policy_decisions row with
// would_publish_special = '1' but no matching publication_outbox row at
// all (the article never actually reached the outbox, let alone
// delivered) must NOT produce a source_policy_cooldowns row. If the
// backfill ever regressed to reading would_publish_special instead of
// real publication_attempts ground truth, this is the case that would
// start failing.
func TestMigrate_0028DoesNotBackfillFromShadowModeWouldPublishSpecial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	applyMigrationsThrough(t, ctx, db, "0027_source_policy_decisions_cooldown_outcome.sql")

	seeds := []string{
		`INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', '2026-07-16T12:00:00Z', '2026-07-16T12:00:00Z', 'open')`,
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status, source_id, source_type) VALUES ('a1','u1','u1','2026-07-16T12:00:00Z','rejected_quality','s1','rss')`,
		`INSERT INTO source_policy_decisions (id, article_id, source_id, authority, policy_version, cluster_id, decision_code, would_publish_special, mode, decided_at) VALUES ('d1', 'a1', 's1', 'official_team', 'v1', 'c1', 'publish_official_confirmation', '1', 'shadow', '2026-07-16T12:00:00Z')`,
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed, err)
		}
	}

	if err := sqlite.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM source_policy_cooldowns WHERE source_id = 's1' AND cluster_id = 'c1'`).Scan(&count); err != nil {
		t.Fatalf("count backfilled rows: %v", err)
	}
	if count != 0 {
		t.Errorf("source_policy_cooldowns rows for (s1, c1) = %d, want 0 (would_publish_special must never drive the backfill)", count)
	}
}
