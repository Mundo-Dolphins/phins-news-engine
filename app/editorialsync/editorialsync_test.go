package editorialsync_test

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialsync"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var migratedSnapshotTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-editorialsync-test-template-")
	if err != nil {
		panic("create SQLite test template directory: " + err.Error())
	}

	migratedSnapshotTemplate = filepath.Join(templateDir, "migrated.db")
	db, err := infrasqlite.Open(migratedSnapshotTemplate, 5*time.Second)
	if err != nil {
		_ = os.RemoveAll(templateDir)
		panic("open SQLite test template: " + err.Error())
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("migrate SQLite test template: " + err.Error())
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("checkpoint SQLite test template: " + err.Error())
	}
	if err := db.Close(); err != nil {
		_ = os.RemoveAll(templateDir)
		panic("close SQLite test template: " + err.Error())
	}

	exitCode := m.Run()
	_ = os.RemoveAll(templateDir)
	os.Exit(exitCode)
}

func copyMigratedSnapshot(t testing.TB, path string) {
	t.Helper()
	source, err := os.Open(migratedSnapshotTemplate)
	if err != nil {
		t.Fatalf("open migrated SQLite test template: %v", err)
	}
	defer source.Close()

	destination, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create SQLite test snapshot: %v", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatalf("copy migrated SQLite test template: %v", err)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("close copied SQLite test snapshot: %v", err)
	}
}

// fakeRemoteSync adapts a plain function to out.RemoteSync, matching the
// test convention already used in cli/cmd/editorialcycle_test.go.
type fakeRemoteSync func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error)

func (f fakeRemoteSync) Sync(ctx context.Context, req out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
	return f(ctx, req)
}

// writeMigratedSnapshot copies a real, migrated SQLite database to path
// and returns a checksum/size-matching out.RemoteSyncResult for it, as a
// real (fake-transport) sync would report.
func writeMigratedSnapshot(t *testing.T, path string) out.RemoteSyncResult {
	t.Helper()
	copyMigratedSnapshot(t, path)
	return out.RemoteSyncResult{IncomingPath: path, SnapshotCreatedAt: time.Now().UTC()}
}

func baseRequest(localDBPath string, migrationsFS embed.FS) editorialsync.Request {
	return editorialsync.Request{
		Host: "h", RemoteDBHint: "r", LocalDBPath: localDBPath, RetentionDays: 7,
		BusyTimeout: 5 * time.Second, MigrationsFS: migrationsFS,
	}
}

// TestRun_InodeReplacement_ReviewSeesTheFreshSnapshot is the core
// repro of an earlier bug, now permanent: two databases, each carrying a
// distinguishing marker row (a source with a different name), prove Run
// hands back a connection that reads the NEW snapshot's marker, never the
// old one -- because Run never opens req.LocalDBPath until after the
// incoming file has already been promoted into place.
func TestRun_InodeReplacement_ReviewSeesTheFreshSnapshot(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	copyMigratedSnapshot(t, localDBPath)

	oldDB, err := infrasqlite.Open(localDBPath, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := oldDB.Exec(`INSERT INTO sources (id, name, type) VALUES ('s1', 'OLD-MARKER', 'rss')`); err != nil {
		t.Fatalf("seed old marker: %v", err)
	}
	oldDB.Close() // simulates the pre-fix bug's absence: nothing holds this open across Run

	incomingPath := filepath.Join(dir, "incoming.db")
	incomingResult := writeMigratedSnapshot(t, incomingPath)
	incomingDB, err := sql.Open("sqlite", incomingPath)
	if err != nil {
		t.Fatalf("open incoming for seeding: %v", err)
	}
	if _, err := incomingDB.Exec(`INSERT INTO sources (id, name, type) VALUES ('s1', 'NEW-MARKER', 'rss')`); err != nil {
		t.Fatalf("seed new marker: %v", err)
	}
	incomingDB.Close()

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return incomingResult, nil
	})}

	db, result, err := editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	defer db.Close()

	var name string
	if err := db.QueryRow(`SELECT name FROM sources WHERE id = 's1'`).Scan(&name); err != nil {
		t.Fatalf("query marker: %v", err)
	}
	if name != "NEW-MARKER" {
		t.Errorf("marker = %q, want %q (Run's connection must read the just-promoted snapshot)", name, "NEW-MARKER")
	}
	if !result.Promoted || !result.PreviousBackedUp || !result.Merged || !result.Migrated {
		t.Errorf("result = %+v, want every step to have run", result)
	}
}

func TestRun_FirstEverSync_NoExistingLocalDB_SkipsMergeButSucceeds(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	incomingResult := writeMigratedSnapshot(t, filepath.Join(dir, "incoming.db"))

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return incomingResult, nil
	})}

	db, result, err := editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	defer db.Close()
	if !result.Promoted || result.PreviousBackedUp || result.Merged != false || !result.Migrated {
		t.Errorf("result = %+v, want Promoted+Migrated true, PreviousBackedUp+Merged false (nothing to merge)", result)
	}
}

func TestRun_SyncFails_LocalDBUntouched(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	writeMigratedSnapshot(t, localDBPath) // pre-existing local state

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return out.RemoteSyncResult{}, errors.New("ssh: connection refused")
	})}

	_, _, err := editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err == nil {
		t.Fatal("Run() error = nil, want the sync failure surfaced")
	}
	db, err := sql.Open("sqlite", localDBPath)
	if err != nil {
		t.Fatalf("open local db after failed sync: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count == 0 {
		t.Error("local db appears empty/replaced after a failed sync, want it left exactly as it was")
	}
}

// TestRun_NonexistentIncomingPath_RejectedBeforeSchemaCheck_LeavesLocalDBUntouched
// guards against sql.Open's SQLite driver lazily creating an empty file
// at IncomingPath: a transport bug (or misbehaving fake) that reports a
// bogus/nonexistent path must be rejected outright, not silently pass
// CheckSchemaCompatible against a freshly-created, empty database.
func TestRun_NonexistentIncomingPath_RejectedBeforeSchemaCheck_LeavesLocalDBUntouched(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	writeMigratedSnapshot(t, localDBPath)

	nonexistentPath := filepath.Join(dir, "does-not-exist.db")
	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return out.RemoteSyncResult{IncomingPath: nonexistentPath}, nil
	})}

	_, _, err := editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err == nil {
		t.Fatal("Run() error = nil, want a nonexistent IncomingPath rejected")
	}
	if _, statErr := os.Stat(nonexistentPath); statErr == nil {
		t.Error("IncomingPath now exists on disk — Run must not let sql.Open lazily create it")
	}

	db, err := sql.Open("sqlite", localDBPath)
	if err != nil {
		t.Fatalf("open local db after rejection: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count == 0 {
		t.Error("local db appears empty/replaced after a rejected sync, want it left exactly as it was")
	}
}

func TestRun_IncompatibleSchema_RejectedBeforePromotion_LeavesLocalDBUntouched(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	writeMigratedSnapshot(t, localDBPath)

	// A snapshot claiming a schema_migrations version this build's
	// migrations.FS does not know about.
	incomingPath := filepath.Join(dir, "incoming.db")
	copyMigratedSnapshot(t, incomingPath)
	incomingDB, err := infrasqlite.Open(incomingPath, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := incomingDB.Exec(`INSERT INTO schema_migrations (version) VALUES ('9999_from_the_future.sql')`); err != nil {
		t.Fatalf("seed future migration marker: %v", err)
	}
	incomingDB.Close()

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return out.RemoteSyncResult{IncomingPath: incomingPath}, nil
	})}

	_, _, err = editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err == nil {
		t.Fatal("Run() error = nil, want the incompatible-schema snapshot rejected")
	}

	db, err := sql.Open("sqlite", localDBPath)
	if err != nil {
		t.Fatalf("open local db after rejection: %v", err)
	}
	defer db.Close()
	var futureCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = '9999_from_the_future.sql'`).Scan(&futureCount); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if futureCount != 0 {
		t.Error("local db was promoted despite CheckSchemaCompatible rejecting it")
	}
}

func TestRun_MergeFailure_RollsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	copyMigratedSnapshot(t, localDBPath)
	oldDB, err := infrasqlite.Open(localDBPath, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := oldDB.Exec(`INSERT INTO sources (id, name, type) VALUES ('s1', 'PRE-EXISTING', 'rss')`); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	// A damaged local schema forces the merge to fail even after the
	// incoming snapshot has been migrated successfully.
	if _, err := oldDB.Exec(`DROP TABLE human_label_conflicts`); err != nil {
		t.Fatalf("damage local schema: %v", err)
	}
	oldDB.Close()

	incomingPath := filepath.Join(dir, "incoming.db")
	writeMigratedSnapshot(t, incomingPath)

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return out.RemoteSyncResult{IncomingPath: incomingPath}, nil
	})}

	_, _, err = editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err == nil {
		t.Fatal("Run() error = nil, want the merge failure (missing human_label_conflicts table) surfaced")
	}

	db, err := sql.Open("sqlite", localDBPath)
	if err != nil {
		t.Fatalf("open local db after rollback: %v", err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM sources WHERE id = 's1'`).Scan(&name); err != nil {
		t.Fatalf("query marker after rollback: %v (want the pre-sync database restored)", err)
	}
	if name != "PRE-EXISTING" {
		t.Errorf("marker = %q, want %q (rolled back to the pre-sync database)", name, "PRE-EXISTING")
	}
}

func TestRun_ChecksumMismatch_RejectedBeforePromotion(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	copyMigratedSnapshot(t, localDBPath)
	oldDB, err := infrasqlite.Open(localDBPath, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := oldDB.Exec(`INSERT INTO sources (id, name, type) VALUES ('s1', 'PRE-EXISTING', 'rss')`); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	oldDB.Close()

	incomingPath := filepath.Join(dir, "incoming.db")
	writeMigratedSnapshot(t, incomingPath)

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return out.RemoteSyncResult{IncomingPath: incomingPath, ChecksumSHA256: "not-the-real-checksum"}, nil
	})}

	_, _, err = editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err == nil {
		t.Fatal("Run() error = nil, want a checksum-mismatch rejection")
	}

	db, err := sql.Open("sqlite", localDBPath)
	if err != nil {
		t.Fatalf("open local db after rejection: %v", err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM sources WHERE id = 's1'`).Scan(&name); err != nil {
		t.Fatalf("query marker: %v", err)
	}
	if name != "PRE-EXISTING" {
		t.Errorf("marker = %q, want %q (a checksum mismatch must be rejected before promotion ever touches LOCAL_DB)", name, "PRE-EXISTING")
	}
}

func TestRun_HandlesClosedAfterEveryOutcome(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")
	writeMigratedSnapshot(t, localDBPath) // pre-existing local state, so Promote actually creates a backup slot
	incomingResult := writeMigratedSnapshot(t, filepath.Join(dir, "incoming.db"))

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return incomingResult, nil
	})}
	db, _, err := editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// The backup slot must exist as a plain, independently-accessible
	// file -- proving Run never held its own handle open on it past the
	// merge step (an open, unreleased handle would not stop os.Stat from
	// seeing the file, but would leak across repeated cycle runs; this
	// combined with the race-detector run in Step 5 is what actually
	// proves no handle leaks).
	if _, err := os.Stat(localDBPath + ".previous"); err != nil {
		t.Errorf("backup slot %s.previous missing after a successful run: %v", localDBPath, err)
	}
}

// TestRun_PromoteSidecarFailureAfterBackup_RollsBackToBackup covers a
// Promote failure that happens AFTER the backup has already been made --
// e.g. the main database file was successfully renamed into the backup
// slot, but a -wal sidecar rename then failed (see
// TestPromote_SidecarRenameFailureAfterMainRename_ReturnsAccurateBackupPath
// in infrastructure/sqlite/promote_test.go for the same fixture at the
// Promote level). In that case Promote still goes on to rename the
// incoming snapshot into LocalDBPath, so LocalDBPath ends up holding the
// new, unmerged snapshot while the real, previously-merged database sits
// at the backup slot -- Run must roll back to the backup rather than
// treating this as a plain terminal error, or the next cycle's Promote
// would overwrite the single backup slot with the unmerged snapshot,
// permanently destroying the editorial review corpus.
func TestRun_PromoteSidecarFailureAfterBackup_RollsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	localDBPath := filepath.Join(dir, "local.db")

	// Built with a plain (non-WAL) connection, so its persisted header
	// never claims WAL journaling -- keeping the post-rollback re-open
	// below immune to the stray -wal sidecar this test deliberately
	// leaves behind.
	oldDB, err := sql.Open("sqlite", localDBPath)
	if err != nil {
		t.Fatalf("open local db: %v", err)
	}
	if err := infrasqlite.Migrate(context.Background(), oldDB, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := oldDB.Exec(`INSERT INTO sources (id, name, type) VALUES ('s1', 'PRE-SIDECAR-FAILURE', 'rss')`); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	oldDB.Close()

	// A stray -wal sidecar sitting next to the local db, matching
	// Promote's own test fixture for forcing a sidecar rename failure.
	if err := os.WriteFile(localDBPath+"-wal", []byte("stale-wal"), 0o644); err != nil {
		t.Fatalf("seed stale wal sidecar: %v", err)
	}

	// Block the backup slot's own -wal destination with a directory, so
	// Promote's main-file rename succeeds (backedUp will be true) but the
	// -wal sidecar rename then fails.
	backupPath := localDBPath + ".previous"
	if err := os.Mkdir(backupPath+"-wal", 0o755); err != nil {
		t.Fatalf("seed blocking dir: %v", err)
	}

	incomingResult := writeMigratedSnapshot(t, filepath.Join(dir, "incoming.db"))

	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return incomingResult, nil
	})}

	_, _, err = editorialsync.Run(context.Background(), deps, baseRequest(localDBPath, migrations.FS))
	if err == nil {
		t.Fatal("Run() error = nil, want the sidecar-rename Promote failure surfaced")
	}

	db, err := sql.Open("sqlite", localDBPath)
	if err != nil {
		t.Fatalf("open local db after rollback: %v", err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM sources WHERE id = 's1'`).Scan(&name); err != nil {
		t.Fatalf("query marker after rollback: %v (want the pre-sync database restored, not left holding the unmerged incoming snapshot)", err)
	}
	if name != "PRE-SIDECAR-FAILURE" {
		t.Errorf("marker = %q, want %q (a Promote failure after a successful backup must roll back to the pre-sync database)", name, "PRE-SIDECAR-FAILURE")
	}
}

// Model the 16-column schema before migrations 0041 and 0043. Other
// migrations remain applied so upgrading runs the real pending SQL files.
func downgradeEntityProposals(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, column := range []string{"verification_version", "verification_provider_id", "verification_provider", "decision_reason"} {
		if _, err := db.Exec(`ALTER TABLE entity_proposals DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version IN ('0041_entity_proposals_decision_reason.sql', '0043_proposal_verification.sql')`); err != nil {
		t.Fatalf("remove migration records: %v", err)
	}
}

func TestRun_MergesDifferentEntityProposalSchemas(t *testing.T) {
	for _, olderLocal := range []bool{false, true} {
		name := "older_remote"
		if olderLocal {
			name = "older_local"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			localPath := filepath.Join(dir, "local.db")
			incomingPath := filepath.Join(dir, "incoming.db")
			for _, path := range []string{localPath, incomingPath} {
				copyMigratedSnapshot(t, path)
				db, err := infrasqlite.Open(path, 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				isLocal := path == localPath
				if isLocal == olderLocal {
					downgradeEntityProposals(t, db)
				}
				id := "remote-proposal"
				if isLocal {
					id = "local-proposal"
				}
				if _, err := db.Exec(`INSERT INTO entity_proposals
					(id, canonical_name, normalized_name, entity_type, kind, status, first_seen, last_seen, extractor_version, created_at)
					VALUES (?, ?, ?, 'person', 'new', 'approved', ?, ?, 'v1', ?)`, id, id, id, time.Now(), time.Now(), time.Now()); err != nil {
					t.Fatal(err)
				}
				if isLocal && !olderLocal {
					if _, err := db.Exec(`UPDATE entity_proposals SET decision_reason = 'verified', verification_provider = 'wikidata', verification_provider_id = 'Q123', verification_version = 'v1'`); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			backupBytes, err := os.ReadFile(localPath)
			if err != nil {
				t.Fatal(err)
			}
			deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
				return out.RemoteSyncResult{IncomingPath: incomingPath}, nil
			})}
			db, result, err := editorialsync.Run(context.Background(), deps, baseRequest(localPath, migrations.FS))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			defer db.Close()
			if !result.Migrated || !result.Merged {
				t.Fatalf("result = %+v, want migrated and merged", result)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM entity_proposals WHERE status = 'approved'`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("proposal count = %d, error = %v, want 2", count, err)
			}
			var reason, provider, providerID, version string
			if err := db.QueryRow(`SELECT decision_reason, verification_provider, verification_provider_id, verification_version FROM entity_proposals WHERE id = 'local-proposal'`).Scan(&reason, &provider, &providerID, &version); err != nil {
				t.Fatal(err)
			}
			if olderLocal {
				if reason != "" || provider != "" || providerID != "" || version != "" {
					t.Fatalf("new columns should retain empty defaults: %q %q %q %q", reason, provider, providerID, version)
				}
			} else if reason != "verified" || provider != "wikidata" || providerID != "Q123" || version != "v1" {
				t.Fatalf("lost local verification metadata: %q %q %q %q", reason, provider, providerID, version)
			}
			gotBackup, err := os.ReadFile(localPath + ".previous")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotBackup, backupBytes) {
				t.Fatal("sync changed the backup database")
			}
		})
	}
}

func TestRun_MigrationFailure_RollsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "local.db")
	copyMigratedSnapshot(t, localPath)
	original, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	incomingPath := filepath.Join(dir, "incoming.db")
	db, err := sql.Open("sqlite", incomingPath)
	if err != nil {
		t.Fatal(err)
	}
	// The first migration cannot index this malformed articles table.
	if _, err := db.Exec(`CREATE TABLE articles (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	deps := editorialsync.Deps{Sync: fakeRemoteSync(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
		return out.RemoteSyncResult{IncomingPath: incomingPath}, nil
	})}
	gotDB, result, err := editorialsync.Run(context.Background(), deps, baseRequest(localPath, migrations.FS))
	if gotDB != nil {
		_ = gotDB.Close()
		t.Fatal("Run returned a database after failed migration")
	}
	if err == nil || !strings.Contains(err.Error(), "editorialsync: migrate:") {
		t.Fatalf("Run error = %v, want migration failure", err)
	}
	if !result.Promoted || !result.PreviousBackedUp || result.Migrated || result.Merged {
		t.Fatalf("result = %+v, want promotion followed by failed migration before merge", result)
	}
	restored, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatal("migration failure did not restore the original local database")
	}
}
