package editorial_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	domaintelegramimport "github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// fakeRemoteSyncFunc adapts a plain function to out.RemoteSync, letting
// tests inject sync behavior without shelling out to the real script.
type fakeRemoteSyncFunc func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error)

func (f fakeRemoteSyncFunc) Sync(ctx context.Context, req out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
	return f(ctx, req)
}

// writeMigratedIncomingSnapshot creates a real, migrated SQLite database
// at a temp path and returns a fake sync's out.RemoteSyncResult for it —
// editorialsync.Run (Task 6) performs a real promote/merge/migrate even
// under a faked transport, so every test that does NOT pass --skip-sync
// needs its fake to hand back a real, promotable file.
func writeMigratedIncomingSnapshot(t *testing.T) out.RemoteSyncResult {
	t.Helper()
	path := filepath.Join(t.TempDir(), "incoming.db")
	db, err := infrasqlite.Open(path, 5000)
	if err != nil {
		t.Fatalf("open incoming: %v", err)
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate incoming: %v", err)
	}
	db.Close()
	return out.RemoteSyncResult{IncomingPath: path}
}

func seedReviewCandidate(t *testing.T, dbPath, articleID string) {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	// Use the real current time rather than a fixed calendar date: the
	// command's Since window is clock.Real{}.Now().AddDate(0, 0, -30), a
	// real wall-clock cutoff, so a hardcoded past date would eventually
	// (and, as of this writing, already does) fall outside that window.
	now := time.Now().UTC()
	if err := articles.Save(context.Background(), article.Article{
		ID: articleID, OriginalURL: "https://example.com/" + articleID, CanonicalURL: "https://example.com/" + articleID,
		Title: "Isotopes sign veteran CB", FetchedAt: now, PublishedAt: now, Status: article.StatusPendingPublication,
		Source: source.Source{ID: mustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
}

// dumpRowCounts returns a per-table row count snapshot of every
// application table at dbPath — used to prove --dry-run leaves the
// database byte-for-byte (row-for-row) unchanged, per the design spec's
// §13.2 "compare checksums/rows before and after dry-run" requirement.
func dumpRowCounts(t *testing.T, dbPath string) map[string]int {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	rows.Close()

	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var n int
		if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(1) FROM %q`, table)).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// seedPreEditorialCycleLifecycleSchema creates a real SQLite database
// migrated only through 0023_outbox_delivery_guarantee.sql — i.e. before
// this branch's 0024/0025 migrations (editorial_cycle_sessions'
// total_candidates column, added by 0025) ever ran. It cannot use
// infrasqlite.Migrate, which always applies every migration a build
// knows about; instead it replays each pre-0024 migration file directly,
// mirroring infrastructure/sqlite.Migrate's own foreign-key-off bracket
// for the same reason that package's doc comment gives (some earlier
// migrations, e.g. 0017/0023, rebuild a table under enforcement
// temporarily disabled).
//
// This exists to reproduce Important finding #2 for real: on a database
// this stale, editorialcycle.BuildPlan's GetLatestResumable selects a
// total_candidates column that does not exist, and SQLite fails the
// query with a raw "no such column: total_candidates" error — exactly
// the failure --dry-run --skip-sync's already-computed pending-migration
// warning exists to make actionable instead of cryptic.
func seedPreEditorialCycleLifecycleSchema(t *testing.T, dbPath string) {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if e.Name() >= "0024" {
			continue // stop before the editorial-cycle-session-lifecycle migrations
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		contents, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatalf("disable foreign keys for migration %s: %v", name, err)
		}
		if _, err := db.Exec(string(contents)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatalf("record migration %s: %v", name, err)
		}
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			t.Fatalf("re-enable foreign keys after migration %s: %v", name, err)
		}
	}
}

type failingDeployer struct{}

func (failingDeployer) Deploy(context.Context, out.RemoteDeployRequest) error {
	return errors.New("fake deployer: unexpected Deploy call")
}

func (failingDeployer) Check(context.Context, out.RemoteHealthCheckRequest) (bool, string, error) {
	return false, "", errors.New("fake deployer: unexpected Check call")
}

func (failingDeployer) Rollback(context.Context, out.RemoteDeployRequest) error {
	return errors.New("fake deployer: unexpected Rollback call")
}

// installFakeRemoteOps registers explicit fakes (sync as given, deploy always
// failing) and unregisters them when the test ends.
func installFakeRemoteOps(t *testing.T, newSync func(string, string) out.RemoteSync) {
	t.Helper()
	shared.SetRemoteOps(shared.RemoteOps{
		NewSync:   newSync,
		NewDeploy: func(_, _, _, _ string) shared.RemoteDeployer { return failingDeployer{} },
	})
	t.Cleanup(shared.ClearRemoteOps)
}

func TestEditorialCycleCmd_SkipSync_NeverCallsSync(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")

	called := false
	installFakeRemoteOps(t, func(string, string) out.RemoteSync {
		return fakeRemoteSyncFunc(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
			called = true
			return out.RemoteSyncResult{}, nil
		})
	})

	_, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--non-interactive")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if called {
		t.Error("Sync was called despite --skip-sync")
	}
}

// TestEditorialCycleCmd_SyncStep_UsesConfiguredScriptPath pins the remote sync
// script host setting: the neutral library default, then the host override.
func TestEditorialCycleCmd_SyncStep_UsesConfiguredScriptPath(t *testing.T) {
	t.Cleanup(func() { shared.RemoteSyncScriptPath = shared.DefaultRemoteSyncScriptPath })
	for _, tc := range []struct{ name, override, want string }{
		{"default", "", "scripts/sync-remote-sqlite.sh"},
		{"override", "scripts/host-sync.sh", "scripts/host-sync.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared.RemoteSyncScriptPath = shared.DefaultRemoteSyncScriptPath
			if tc.override != "" {
				shared.RemoteSyncScriptPath = tc.override
			}
			configPath := testsupport.WriteFixture(t)
			dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
			seedReviewCandidate(t, dbPath, "a1")

			var gotScript string
			installFakeRemoteOps(t, func(script, _ string) out.RemoteSync {
				gotScript = script
				return fakeRemoteSyncFunc(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
					return writeMigratedIncomingSnapshot(t), nil
				})
			})

			_, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-deploy", "--remote", "deploy@host", "--non-interactive", "--skip-dictionary", "--skip-train")
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if gotScript != tc.want {
				t.Errorf("sync script = %q, want %q", gotScript, tc.want)
			}
		})
	}
}

// TestEditorialCycleCmd_NoTelegramCredentials_FailsBeforeAnyDatabaseWrite
// pins the strict-by-default policy: missing Telegram
// credentials must fail the whole run, before sync or any other database
// write, rather than silently proceeding without the import.
func TestEditorialCycleCmd_NoTelegramCredentials_FailsBeforeAnyDatabaseWrite(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")

	syncCalled := false
	installFakeRemoteOps(t, func(string, string) out.RemoteSync {
		return fakeRemoteSyncFunc(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
			syncCalled = true
			return out.RemoteSyncResult{}, nil
		})
	})

	_, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-deploy", "--remote", "deploy@host", "--non-interactive")
	if err == nil {
		t.Fatal("Execute() error = nil, want a failure when Telegram credentials are missing and --skip-telegram-import was not passed")
	}
	if !strings.Contains(err.Error(), "skip-telegram-import") {
		t.Errorf("error = %q, want it to mention --skip-telegram-import as the escape hatch", err.Error())
	}
	if syncCalled {
		t.Error("sync was called despite the missing-credentials failure — must fail before touching the database")
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("stat(%s) error = %v, want the local database file untouched", dbPath, statErr)
	}
}

// TestEditorialCycleCmd_SkipTelegramImport_WarnsAndRecordsInReport pins the
// only sanctioned bypass: --skip-telegram-import must print a visible
// warning and record the decision (and its reason) in the session's audit
// report, never switch to skip silently.
func TestEditorialCycleCmd_SkipTelegramImport_WarnsAndRecordsInReport(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")

	out, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--non-interactive")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "--skip-telegram-import") {
		t.Errorf("output = %q, want a visible warning naming --skip-telegram-import", out)
	}
	if !strings.Contains(out, "Telegram import: SKIPPED") {
		t.Errorf("output = %q, want the audit summary to record the skip", out)
	}

	report := readLatestAuditReport(t, filepath.Join(filepath.Dir(dbPath), "audit"))
	if skipped, _ := report["TelegramImportSkipped"].(bool); !skipped {
		t.Errorf("audit report = %+v, want TelegramImportSkipped=true", report)
	}
	if reason, _ := report["TelegramImportSkipReason"].(string); reason == "" {
		t.Errorf("audit report = %+v, want a non-empty TelegramImportSkipReason", report)
	}
}

// fakeTelegramImporter fakes out.TelegramChannelImporter so `editorial
// cycle`'s Telegram-history step can be exercised without a live MTProto
// login — see shared.NewTelegramImportClient's doc comment.
type fakeTelegramImporter struct {
	messages []domaintelegramimport.Message
	err      error
}

func (f fakeTelegramImporter) ExportChannelMessages(context.Context, out.TelegramImportRequest) ([]domaintelegramimport.Message, error) {
	return f.messages, f.err
}

// telegramImportEnv sets the minimum env/config needed to pass
// shared.ValidateTelegramImportConfig (api_id, api_hash, phone, and the
// fixture's already-configured channel_id).
func telegramImportEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TELEGRAM_API_ID", "12345")
	t.Setenv("TELEGRAM_API_HASH", "test-hash")
	t.Setenv("TELEGRAM_PHONE", "+15551234567")
}

func swapTelegramImportClient(t *testing.T, importer out.TelegramChannelImporter, clientErr error) {
	t.Helper()
	original := shared.NewTelegramImportClient
	shared.NewTelegramImportClient = func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error) {
		return importer, clientErr
	}
	t.Cleanup(func() { shared.NewTelegramImportClient = original })
}

// TestEditorialCycleCmd_TelegramImportSuccess_RecordsCountsInReport proves
// a successful import's counts reach both stdout and the audit report,
// using the new vocabulary (processed/imported/already_known/unmatched/
// skipped/failed) rather than the old matched/already_labeled/unmatched
// fields.
func TestEditorialCycleCmd_TelegramImportSuccess_RecordsCountsInReport(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")
	telegramImportEnv(t)
	swapTelegramImportClient(t, fakeTelegramImporter{
		messages: []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/a1"}},
	}, nil)

	out, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-sync", "--skip-deploy", "--non-interactive", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "telegram import: processed=1 imported=1 already_known=0 unmatched=0 skipped=0 failed=0") {
		t.Errorf("output = %q, want the new field vocabulary with processed=1 imported=1", out)
	}

	report := readLatestAuditReport(t, filepath.Join(filepath.Dir(dbPath), "audit"))
	if skipped, _ := report["TelegramImportSkipped"].(bool); skipped {
		t.Error("audit report TelegramImportSkipped = true, want false — the import ran")
	}
	if got, _ := report["TelegramImportImported"].(float64); got != 1 {
		t.Errorf("audit report TelegramImportImported = %v, want 1", got)
	}
}

// TestEditorialCycleCmd_DryRun_TelegramImportStillFetchesButNeverWrites
// pins the --dry-run interaction with the Telegram-history step: the
// channel is still fetched and matched (so the operator sees accurate
// counts of what WOULD happen), but no human_labels row is ever written —
// mirroring application/telegramimport.Import's own dry-run contract.
func TestEditorialCycleCmd_DryRun_TelegramImportStillFetchesButNeverWrites(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")
	telegramImportEnv(t)
	fetchCalled := false
	swapTelegramImportClient(t, fakeTelegramImporterFunc(func() ([]domaintelegramimport.Message, error) {
		fetchCalled = true
		return []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/a1"}}, nil
	}), nil)

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-sync", "--skip-deploy", "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !fetchCalled {
		t.Error("ExportChannelMessages was never called — --dry-run must still fetch Telegram history")
	}
	if !strings.Contains(out, "telegram import: processed=1 imported=1 already_known=0 unmatched=0 skipped=0 failed=0") {
		t.Errorf("output = %q, want dry-run to report imported=1 (what WOULD happen)", out)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	saved, err := labels.ListByArticle(context.Background(), "a1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(saved) != 0 {
		t.Errorf("human_labels for a1 = %+v, want none written — --dry-run must never write", saved)
	}
}

// fakeTelegramImporterFunc adapts a plain function to
// out.TelegramChannelImporter, letting a test observe whether/how many
// times ExportChannelMessages was actually invoked.
type fakeTelegramImporterFunc func() ([]domaintelegramimport.Message, error)

func (f fakeTelegramImporterFunc) ExportChannelMessages(context.Context, out.TelegramImportRequest) ([]domaintelegramimport.Message, error) {
	return f()
}

// TestEditorialCycleCmd_TelegramImportFailure_AbortsWithAuditReport proves
// a real import failure aborts the whole run (nonzero exit) while still
// writing an audit report that records the failure — never a false
// "success" for a run whose required source failed.
func TestEditorialCycleCmd_TelegramImportFailure_AbortsWithAuditReport(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")
	telegramImportEnv(t)
	swapTelegramImportClient(t, fakeTelegramImporter{err: fmt.Errorf("database is locked")}, nil)

	out, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-sync", "--skip-deploy", "--non-interactive")
	if err == nil {
		t.Fatal("Execute() error = nil, want the injected telegram import failure to abort the run")
	}
	if strings.Contains(out, "review complete") {
		t.Errorf("output = %q, must not claim success when telegram import failed", out)
	}

	report := readLatestAuditReport(t, filepath.Join(filepath.Dir(dbPath), "audit"))
	if errMsg, _ := report["Error"].(string); errMsg == "" {
		t.Errorf("audit report = %+v, want a non-empty Error field", report)
	}
	if failed, _ := report["TelegramImportFailed"].(float64); failed != 1 {
		t.Errorf("audit report TelegramImportFailed = %v, want 1", failed)
	}
}

// TestEditorialCycleCmd_TelegramClientConstructionFailure_RecordsFailedInReport
// covers the other failure path — building the Telegram client itself
// fails (e.g. a transient auth/session problem) before any message is
// ever fetched — and pins that it is also recorded as a failure in the
// audit report, not left at TelegramImportFailed=0 alongside a populated
// Error (which the earlier ExportChannelMessages-failure path already
// covers).
func TestEditorialCycleCmd_TelegramClientConstructionFailure_RecordsFailedInReport(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")
	telegramImportEnv(t)
	swapTelegramImportClient(t, nil, fmt.Errorf("session file is corrupt"))

	_, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-sync", "--skip-deploy", "--non-interactive")
	if err == nil {
		t.Fatal("Execute() error = nil, want the injected client-construction failure to abort the run")
	}

	report := readLatestAuditReport(t, filepath.Join(filepath.Dir(dbPath), "audit"))
	if errMsg, _ := report["Error"].(string); errMsg == "" {
		t.Errorf("audit report = %+v, want a non-empty Error field", report)
	}
	if failed, _ := report["TelegramImportFailed"].(float64); failed != 1 {
		t.Errorf("audit report TelegramImportFailed = %v, want 1", failed)
	}
}

// readLatestAuditReport reads the most recently written audit JSON file
// in dir (editorialcycle.WriteAuditReport's output), decoded generically
// so tests can assert on individual fields without importing the
// application package's concrete type here.
func readLatestAuditReport(t *testing.T, dir string) map[string]any {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("no audit report files found in %s", dir)
	}
	var latest string
	var latestMod time.Time
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("Info() error = %v", err)
		}
		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
			latest = e.Name()
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, latest))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", latest, err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", latest, err)
	}
	return report
}

func TestEditorialCycleCmd_MissingRemoteWithoutSkipSyncFails(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	_, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--non-interactive")
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when --remote is missing and --skip-sync/--skip-deploy aren't both set")
	}
}

func TestEditorialCycleCmd_SyncInvokedWithConfigValues(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")

	var gotReq out.RemoteSyncRequest
	installFakeRemoteOps(t, func(string, string) out.RemoteSync {
		return fakeRemoteSyncFunc(func(_ context.Context, req out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
			gotReq = req
			return writeMigratedIncomingSnapshot(t), nil
		})
	})

	_, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-deploy", "--remote", "deploy@host.example.com", "--non-interactive", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotReq.Host != "deploy@host.example.com" {
		t.Errorf("Host = %q, want %q", gotReq.Host, "deploy@host.example.com")
	}
	if gotReq.RemoteDBHint != "~/phins-news-engine/data/operational.db" {
		t.Errorf("RemoteDBHint = %q, want it to come from cfg.Remote.SQLiteRemoteDBHint", gotReq.RemoteDBHint)
	}
	if gotReq.LocalDBPath != dbPath {
		t.Errorf("LocalDBPath = %q, want %q", gotReq.LocalDBPath, dbPath)
	}
	if gotReq.RetentionDays != 7 {
		t.Errorf("RetentionDays = %d, want it to come from cfg.Remote.RetentionDays (7)", gotReq.RetentionDays)
	}
}

// TestEditorialCycleCmd_Sync_PreservesLocalReviewCorpusAcrossSync proves
// the merge step (editorialsync.Run, Task 6) is reachable from the real
// CLI wiring, not just the editorialsync package's own tests: a
// human_labels row written by an earlier run survives a later run that
// syncs (replacing articles/sources with a fresh, unrelated snapshot).
func TestEditorialCycleCmd_Sync_PreservesLocalReviewCorpusAcrossSync(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('prior-cycle', ?, ?, 'completed', 'review', '{}')`, time.Now(), time.Now()); err != nil {
		t.Fatalf("seed prior session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO human_labels (article_id, label, labeled_at) VALUES ('a1', 'publish', ?)`, time.Now()); err != nil {
		t.Fatalf("seed prior label: %v", err)
	}
	db.Close()

	installFakeRemoteOps(t, func(string, string) out.RemoteSync {
		return fakeRemoteSyncFunc(func(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
			return writeMigratedIncomingSnapshot(t), nil
		})
	})

	_, err = testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-deploy", "--remote", "deploy@host", "--non-interactive", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	after, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer after.Close()
	var count int
	if err := after.QueryRow(`SELECT COUNT(1) FROM human_labels WHERE article_id = 'a1' AND label = 'publish'`).Scan(&count); err != nil {
		t.Fatalf("count prior label: %v", err)
	}
	if count != 1 {
		t.Error("prior human_labels row for a1 did not survive the sync")
	}
}

func TestEditorialCycle_DryRun_ListsCandidatesWithoutMutating(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "a1") {
		t.Errorf("output = %q, want it to mention candidate %q", out, "a1")
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("output = %q, want it to say this was a dry run", out)
	}
}

// TestEditorialCycle_DryRun_CreatesNoSession pins the fixed contract for
// the first bug: --dry-run runs the planning phase only and
// writes nothing at all — not a session row, not a decision, not a
// label. (It previously called StartOrResume and left a real "running"
// session behind despite promising to write nothing.)
func TestEditorialCycle_DryRun_CreatesNoSession(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")

	if _, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--dry-run"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var sessionCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM editorial_cycle_sessions`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Errorf("editorial_cycle_sessions row count = %d, want 0 — --dry-run must never create a session", sessionCount)
	}

	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	if _, err := sessions.GetLatestResumable(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetLatestResumable() error = %v, want a wrapped sql.ErrNoRows (no session at all)", err)
	}

	var decisionCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM editorial_review_decisions`).Scan(&decisionCount); err != nil {
		t.Fatalf("count review decisions: %v", err)
	}
	if decisionCount != 0 {
		t.Errorf("editorial_review_decisions row count = %d, want 0 after --dry-run", decisionCount)
	}

	labels := sqliteadapter.NewHumanLabelRepository(db)
	got, err := labels.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListAll() = %+v, want 0 human labels after --dry-run", got)
	}

	// "Writes nothing" covers the audit report file too: --dry-run runs
	// the planning phase only, and a planning-only run has no session to
	// report on.
	if entries, err := os.ReadDir(filepath.Join(filepath.Dir(dbPath), "audit")); err == nil && len(entries) > 0 {
		t.Errorf("audit dir contains %d file(s), want none written by --dry-run", len(entries))
	}
}

func TestEditorialCycle_Interactive_PublishThenSaveAndExit(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")
	seedReviewCandidate(t, dbPath, "a2")

	out, err := testsupport.ExecuteWithStdin(t, "p\nw\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "session saved") {
		t.Errorf("output = %q, want it to confirm the session was saved", out)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	got, err := labels.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(got) != 1 || got[0].Label != "publish" {
		t.Fatalf("ListAll() = %+v, want exactly one \"publish\" label", got)
	}
}

func TestEditorialCycle_Resume_ContinuesWhereItLeftOff(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")
	seedReviewCandidate(t, dbPath, "a2")

	if _, err := testsupport.ExecuteWithStdin(t, "p\nw\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy"); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	out, err := testsupport.ExecuteWithStdin(t, "r\nw\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train", "--resume")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if strings.Contains(out, ">>> a1") {
		t.Errorf("output = %q, want a1 (already decided) not re-presented on resume", out)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	got, err := labels.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListAll() = %+v, want two labels total after resuming and deciding a2", got)
	}
}

func TestEditorialCycle_AllReviewActionsAndControlFlow(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "b1")
	seedReviewCandidate(t, dbPath, "b2")
	seedReviewCandidate(t, dbPath, "b3")

	// b: back at index 0 (no-op, re-shows b1). z: unknown key (re-shows
	// b1, printing the error). k: skip b1 (journaled, no label). b: back
	// from b2 to b1 (re-shows b1). d: duplicate b1 (label=reject). s:
	// same-news b2 (label=reject). e: edit-category b3 (no label). The
	// loop then ends naturally (no "w"), so the session completes.
	out, err := testsupport.ExecuteWithStdin(t, "b\nz\nk\nb\nd\ns\ne\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, `unknown action "z"`) {
		t.Errorf("output = %q, want it to report the unknown key %q", out, "z")
	}
	if !strings.Contains(out, "review complete") {
		t.Errorf("output = %q, want it to confirm the review completed (no save-and-exit)", out)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	got, err := labels.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	gotLabels := map[string]string{}
	for _, e := range got {
		gotLabels[e.ArticleID] = e.Label
	}
	if len(got) != 2 || gotLabels["b1"] != "reject" || gotLabels["b2"] != "reject" {
		t.Fatalf("ListAll() = %+v, want b1 and b2 rejected (duplicate/same-news) and no label for b3 (edit-category)", got)
	}
}

func TestEditorialCycle_AmbiguousAndUpdateActions_JournaledCorrectly(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "au1")
	seedReviewCandidate(t, dbPath, "au2")

	// a: mark au1 ambiguous (journaled, but reviewdecision.Resolve gives a
	// zero-value Outcome for ActionAmbiguous, so Persist is false — no
	// human_labels row). u: mark au2 update (journaled, and
	// reviewdecision.Resolve maps ActionUpdate to
	// label="publish"/reason="substantial_update"/Persist:true, so it does
	// land in human_labels). The loop then ends naturally (no "w"), so the
	// session completes and both decisions are journaled.
	out, err := testsupport.ExecuteWithStdin(t, "a\nu\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "review complete") {
		t.Errorf("output = %q, want it to confirm the review completed", out)
	}

	sessionID := extractStartedSessionID(t, out)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
	decisions, err := reviewDecisions.ListBySession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}
	gotActions := map[string]string{}
	for _, d := range decisions {
		gotActions[d.ArticleID] = d.Action
	}
	if len(decisions) != 2 || gotActions["au1"] != "ambiguous" || gotActions["au2"] != "update" {
		t.Fatalf("ListBySession() = %+v, want au1 journaled as \"ambiguous\" and au2 as \"update\"", decisions)
	}

	labels := sqliteadapter.NewHumanLabelRepository(db)
	got, err := labels.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(got) != 1 || got[0].ArticleID != "au2" || got[0].Label != "publish" || got[0].Reason != "substantial_update" {
		t.Fatalf("ListAll() = %+v, want exactly one label for au2 (publish/substantial_update); au1 (ambiguous) must not be persisted", got)
	}
}

// extractStartedSessionID pulls the session ID out of the "started session
// <id>" / "resuming session <id>" line the cycle command prints, since
// ReviewDecisionRepository has no ListAll — only ListBySession.
func extractStartedSessionID(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		for _, prefix := range []string{"started session ", "resuming session "} {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	t.Fatalf("output = %q, want a %q or %q line", out, "started session ...", "resuming session ...")
	return ""
}

// TestEditorialCycle_BackThenRedecide_SupersedesLabelPreservingHistory
// reproduces the exact bug from the final whole-branch review: a reviewer
// decides r1 (publish), presses "b" (back — the advertised way to correct a
// decision), which re-shows r1 since it's still in the in-memory pending
// slice for this session, then re-decides it (reject). Before the fix this
// left two contradictory active human_labels rows for r1. Under the
// append-only revision-history model, the prior "publish" decision is
// superseded rather than deleted: exactly one active label must survive at
// any time, and it must be "reject" (the latest decision after
// back+re-decide) -- but the superseded "publish" revision correctly
// remains in history rather than being destroyed.
func TestEditorialCycle_BackThenRedecide_SupersedesLabelPreservingHistory(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "r1")
	seedReviewCandidate(t, dbPath, "r2")

	// p: publish r1. b: back from r2 to r1 (re-shows r1). r: re-decide r1
	// as reject. Input then runs out at r2, ending the loop with r2 still
	// undecided, so the session pauses rather than completing (issue
	// #164's second bug — Complete's guard refuses a session with pending
	// candidates). The back+re-decide behavior under test is unaffected
	// either way; the closure message is asserted only to prove the run
	// took the expected path.
	out, err := testsupport.ExecuteWithStdin(t, "p\nb\nr\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "review incomplete — session paused") {
		t.Errorf("output = %q, want it to report the session paused with r2 still undecided", out)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	active, hasActive, err := labels.GetActive(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if !hasActive || active.Label != "reject" {
		t.Fatalf("GetActive(r1) = (%+v, hasActive=%v), want an active label of \"reject\" (the latest decision after back+re-decide)", active, hasActive)
	}

	// Positively demonstrate history is preserved rather than destroyed:
	// the superseded "publish" revision and the new active "reject"
	// revision must both still be present.
	got, err := labels.ListByArticle(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByArticle(r1) = %+v, want exactly two revisions (superseded \"publish\" then active \"reject\")", got)
	}
}

// TestEditorialCycle_BackThenRedecideAmbiguous_ClearsPriorLabel reproduces
// the residual bug from the scoped re-review: a reviewer decides r1
// (publish, which persists a human_labels row), presses "b" (back — the
// advertised way to correct a decision), which re-shows r1, then re-decides
// it "ambiguous" (a non-persisting outcome). "ambiguous" is a
// temporary review cooldown, not a permanent exclusion — but a stale active
// "publish" row left behind here would permanently exclude r1 from
// ListCandidatesForReview even after the cooldown lapses. No ACTIVE
// human_labels row must survive for r1; the retracted "publish" revision
// persisting as inactive history is correct, expected behavior under this
// branch, not a regression.
func TestEditorialCycle_BackThenRedecideAmbiguous_ClearsPriorLabel(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "r1")
	seedReviewCandidate(t, dbPath, "r2")

	// p: publish r1. b: back from r2 to r1 (re-shows r1). a: re-decide r1
	// as ambiguous. Input then runs out at r2, ending the loop with r2
	// still undecided, so the session pauses rather than completing (see
	// TestEditorialCycle_BackThenRedecide_SupersedesLabelPreservingHistory).
	out, err := testsupport.ExecuteWithStdin(t, "p\nb\na\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "review incomplete — session paused") {
		t.Errorf("output = %q, want it to report the session paused with r2 still undecided", out)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	active, hasActive, err := labels.GetActive(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if hasActive {
		t.Fatalf("GetActive(r1) = (%+v, hasActive=%v), want no active label (ambiguous after back must clear the stale active publish row, not just leave it)", active, hasActive)
	}
}

// TestEditorialCycle_StdinReadError_MarksSessionFailedAndReturnsError pins
// the design spec's §8 matrix for a genuine stdin I/O error (as opposed
// to clean EOF): the run returns the wrapped error and the session is
// transitioned to "failed" — never "completed", and never left claiming
// to be "running" with nobody driving it. "failed" is one of
// GetLatestResumable's statuses, so --resume still picks it back up.
func TestEditorialCycle_StdinReadError_MarksSessionFailedAndReturnsError(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "e1")

	_, err := testsupport.ExecuteWithReader(t, errReader{}, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy")
	if err == nil {
		t.Fatal("Execute() error = nil, want a wrapped stdin read error")
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	session, err := sessions.GetLatestResumable(context.Background())
	if err != nil {
		t.Fatalf("GetLatestResumable() error = %v, want the session to remain resumable after a genuine stdin read error", err)
	}
	if session.Status != "failed" {
		t.Errorf("session.Status = %q, want %q (a read error must not finalize the session as completed)", session.Status, "failed")
	}
}

// errReader is an io.Reader that always fails with a non-EOF error, used to
// simulate a genuine stdin read failure (as opposed to clean EOF) in the
// review loop.
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("simulated stdin read failure")
}

// panicOnceWriter panics the first time it is handed a write containing
// marker, then behaves as an ordinary buffer for every write after that.
// It is how TestEditorialCycle_PanicDuringReview_RecoversAndMarksSessionFailed
// injects a panic into the middle of the review loop — the review screen's
// header line is printed after the session exists but before any decision is
// applied — without adding a test-only seam to the production code. Panicking
// only once matters: the deferred audit-report writer prints through this same
// writer after the panic has been recovered, and a second panic there (inside
// a deferred function, after recover has already run) would take the process
// down.
type panicOnceWriter struct {
	marker   string
	panicked bool
	buf      bytes.Buffer
}

func (w *panicOnceWriter) Write(p []byte) (int, error) {
	if !w.panicked && strings.Contains(string(p), w.marker) {
		w.panicked = true
		panic("injected panic during review")
	}
	return w.buf.Write(p)
}

// TestEditorialCycle_PanicDuringReview_RecoversAndMarksSessionFailed pins the
// panic-recovery case: a panic
// after the session exists is recovered, surfaced as an ordinary returned
// error rather than a crash, recorded in the audit report, and — the part this
// test exists for — leaves the session "failed", never "running" with nobody
// driving it.
func TestEditorialCycle_PanicDuringReview_RecoversAndMarksSessionFailed(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "pan1")

	w := &panicOnceWriter{marker: ">>> pan1"}
	cli.Root().SetOut(w)
	cli.Root().SetErr(w)
	cli.Root().SetIn(strings.NewReader("p\n"))
	cli.Root().SetArgs([]string{
		"--config", configPath, "editorial", "cycle",
		"--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train",
	})
	err := cli.Root().Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want the recovered panic surfaced as a normal error")
	}
	if !strings.Contains(err.Error(), "recovered panic") {
		t.Errorf("error = %q, want it to name the recovered panic", err.Error())
	}
	if !w.panicked {
		t.Fatal("the injected panic never fired — the test proved nothing")
	}

	db, openErr := infrasqlite.Open(dbPath, 5000)
	if openErr != nil {
		t.Fatalf("open db: %v", openErr)
	}
	defer db.Close()
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	session, getErr := sessions.GetLatestResumable(context.Background())
	if getErr != nil {
		t.Fatalf("GetLatestResumable() error = %v, want the panicking run's session to still exist", getErr)
	}
	if session.Status != "failed" {
		t.Errorf("session.Status = %q, want %q after a recovered panic", session.Status, "failed")
	}

	report := readLatestAuditReport(t, filepath.Join(filepath.Dir(dbPath), "audit"))
	if errMsg, _ := report["Error"].(string); !strings.Contains(errMsg, "recovered panic") {
		t.Errorf("audit report Error = %q, want it to record the recovered panic", errMsg)
	}
}

// TestEditorialCycle_EOFWithPending_RecordsCompleteRefusalInAuditReport pins
// that Complete's refusal is never swallowed by the pause fallback that
// follows it. That fallback prints a reassuring "paused" line and returns nil
// (exit code 0), which is right for the benign case it was written for — a
// clean EOF partway through the list — but Complete can also fail because its
// pending query or its status write genuinely failed, and both arrive here
// looking identical. The real cause therefore has to reach the audit report
// even on the success-shaped path, or an infrastructure failure would leave no
// trace at all.
func TestEditorialCycle_EOFWithPending_RecordsCompleteRefusalInAuditReport(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "cp1")
	seedReviewCandidate(t, dbPath, "cp2")

	// One "p" answers cp1; input then runs out with cp2 still pending, so
	// Complete refuses and the pause fallback succeeds and returns nil.
	out, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle",
		"--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v, want the pause fallback to succeed", err)
	}
	if !strings.Contains(out, "review incomplete — session paused") {
		t.Fatalf("output = %q, want the pause fallback's message", out)
	}

	report := readLatestAuditReport(t, filepath.Join(filepath.Dir(dbPath), "audit"))
	errMsg, _ := report["Error"].(string)
	if errMsg == "" {
		t.Fatalf("audit report = %+v, want Complete's refusal recorded in Error even though the run exited 0", report)
	}
	if !strings.Contains(errMsg, "still pending") {
		t.Errorf("audit report Error = %q, want it to carry Complete's own reason", errMsg)
	}
}

// TestEditorialCycleCmd_SkipDictionary_NeverRunsDictionaryFlow uses
// testsupport.WriteFixtureWithDictionary (defined in dictionary_test.go) rather than
// plain testsupport.WriteFixture, since the dictionary update step this test exercises
// (or, here, must *not* exercise) needs a loadable dictionary file —
// testsupport.WriteFixture's default fixture leaves dictionary.path empty.
func TestEditorialCycleCmd_SkipDictionary_NeverRunsDictionaryFlow(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "d1")

	out, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out, "candidate term") {
		t.Errorf("output = %q, want dictionary mining skipped", out)
	}
}

func TestEditorialCycleCmd_RunsDictionaryFlowAfterReviewUnlessSkipped(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "d1")

	out, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "candidate term") {
		t.Errorf("output = %q, want dictionary mining to have run against the just-completed session", out)
	}
}

func TestEditorialCycle_NonInteractive_SkipsCleanlyWithoutWriting(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "c1")

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--non-interactive")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "non-interactive") {
		t.Errorf("output = %q, want it to mention --non-interactive", out)
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	labels := sqliteadapter.NewHumanLabelRepository(db)
	got, err := labels.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListAll() = %+v, want no labels written by --non-interactive", got)
	}

	// Important finding #3: skipping the review step must not leave the
	// session "running" with no process driving it — a false state by
	// this branch's own state machine. It must be left
	// "paused" so --resume can pick it back up.
	var status string
	if err := db.QueryRow(`SELECT status FROM editorial_cycle_sessions ORDER BY updated_at DESC LIMIT 1`).Scan(&status); err != nil {
		t.Fatalf("read session status: %v", err)
	}
	if status != "paused" {
		t.Errorf("session status = %q, want %q — --non-interactive with pending candidates must pause, not strand the session running", status, "paused")
	}
}

// TestEditorialCycle_NonInteractive_NoPendingCandidates_FallsThroughToCompletion
// guards against a regression where --non-interactive unconditionally
// returned before the review loop regardless of whether there was
// anything left needing a human decision. When every candidate is
// already labeled (seedTenPublishTenRejectReviewCandidates — see its doc
// comment), the review step has nothing to skip: --non-interactive must
// not print the "skipping the review step" message and must instead fall
// through to editorialcycle.Complete() and the rest of the continuation,
// exactly like a normal interactive run with zero pending candidates
// would.
func TestEditorialCycle_NonInteractive_NoPendingCandidates_FallsThroughToCompletion(t *testing.T) {
	configPath, _ := seedTenPublishTenRejectReviewCandidates(t)

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle",
		"--skip-telegram-import", "--non-interactive", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out, "skipping the review step") {
		t.Errorf("output = %q, want --non-interactive NOT to skip the review step when there are zero pending candidates", out)
	}
	if !strings.Contains(out, "review complete") {
		t.Errorf("output = %q, want the continuation (editorialcycle.Complete and beyond) to have run", out)
	}
}

// TestEditorialCycleCmd_SkipTrain_NeverTrains verifies --skip-train
// bypasses the training/propose-winner/promote continuation entirely —
// neither "no candidate passed gates" (the no-winner message) nor
// "Promote this candidate" (the confirmation prompt) should ever appear.
func TestEditorialCycleCmd_SkipTrain_NeverTrains(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "st1")

	out, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out, "no candidate passed gates") || strings.Contains(out, "Promote this candidate") {
		t.Errorf("output = %q, want training skipped entirely", out)
	}
}

// TestEditorialCycleCmd_SkipDeploy_NeverDeploys verifies --skip-deploy
// bypasses the CI/build/deploy/verify/rollback tail entirely — the
// "ci=... build=... deployed=... healthy=... rolled_back=..." summary line
// this task's wiring prints should never appear.
func TestEditorialCycleCmd_SkipDeploy_NeverDeploys(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "sd1")

	out, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out, "deployed=") {
		t.Errorf("output = %q, want deploy tail skipped", out)
	}
}

// TestEditorialCycleCmd_NonInteractive_SkipDeploy_ReachesAndSkipsDeployTail
// guards against a vacuous pass: with the --non-interactive early-exit bug
// fixed (Task 13), a --non-interactive run against zero pending candidates
// now falls through the review step, dictionary step, and training step
// all the way to this task's deploy tail. This test proves --skip-deploy
// is actually evaluated at that point (the run reaches "review complete"
// and then correctly omits the "deployed=" summary line) rather than
// passing only because --non-interactive exited early before ever
// reaching the new code.
func TestEditorialCycleCmd_NonInteractive_SkipDeploy_ReachesAndSkipsDeployTail(t *testing.T) {
	configPath, _ := seedTenPublishTenRejectReviewCandidates(t)

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle",
		"--skip-telegram-import", "--non-interactive", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "review complete") {
		t.Errorf("output = %q, want the continuation (past the review step) to have run", out)
	}
	if strings.Contains(out, "deployed=") {
		t.Errorf("output = %q, want the deploy tail skipped even though it was reached", out)
	}
}

// seedTenPublishTenRejectReviewCandidates seeds 20 already human-labeled
// articles directly through the repositories (bypassing the interactive
// review step): 10 trivially-publishable ("isotopes win big game today")
// and 10 trivially-rejectable ("sponsored shop now huge discount"), with
// PublishedAt timestamps alternating by day so publish/reject examples
// interleave chronologically and both labels land in every train/
// validate/test partition RunTraining's time-ordered Split produces —
// enough separable signal for even a trivial classifier to clear the
// fixture's default editorial_gates. Every seeded article already has a
// human_labels row, so activelearning's ListCandidatesForReview (which
// excludes any article with an existing label) returns none of them: the
// review loop then runs zero iterations, letting a normal (non-dry-run,
// non-non-interactive) `editorial cycle` invocation fall straight through
// to the training step without consuming any interactive input. Mirrors
// app/editorialcycle/training_test.go's
// seedTenPublishTenReject data shape; this is a fresh copy seeded through
// real sqlite since that package's test cannot be imported here.
func seedTenPublishTenRejectReviewCandidates(t *testing.T) (configPath, dbPath string) {
	t.Helper()
	baseConfigPath := testsupport.WriteFixture(t) // model.path is "" in the base fixture; promotion needs a real path
	dir := filepath.Dir(baseConfigPath)
	configPath = filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, baseConfigPath, configPath, filepath.Join(dir, "models", "editorial", "current"))
	dbPath = filepath.Join(dir, "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	articles := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	base := time.Now().UTC().AddDate(0, 0, -25)

	seed := func(id, title, label string, publishedAt time.Time) {
		t.Helper()
		if err := articles.Save(context.Background(), article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Title: title, FetchedAt: publishedAt, PublishedAt: publishedAt, Status: article.StatusPendingPublication,
			Source: source.Source{ID: mustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
		}); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
		if err := labels.Label(context.Background(), training.Example{ArticleID: id, Label: label, LabeledAt: publishedAt}); err != nil {
			t.Fatalf("seed label %s: %v", id, err)
		}
	}
	for i := 0; i < 10; i++ {
		seed(fmt.Sprintf("p%d", i), fmt.Sprintf("isotopes win big game %d", i), "publish", base.AddDate(0, 0, 2*i))
		seed(fmt.Sprintf("r%d", i), fmt.Sprintf("sponsored shop now huge discount %d", i), "reject", base.AddDate(0, 0, 2*i+1))
	}
	return configPath, dbPath
}

// TestEditorialCycleCmd_WritesAuditReport_OnSuccess proves the audit
// report threaded through RunE's whole continuation (Task 15) is written
// to disk on a clean run: an "audit report: <path>" line is printed, that
// path exists, it parses as an editorialcycle.AuditReport, and its fields
// reflect a review with one published article and no error.
func TestEditorialCycleCmd_WritesAuditReport_OnSuccess(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "aud1")

	out, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	const marker = "audit report: "
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("output = %q, want an %q line", out, marker)
	}
	line := out[idx+len(marker):]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	path := strings.TrimSpace(line)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit report %q: %v", path, err)
	}
	var report editorialcycle.AuditReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal audit report: %v", err)
	}
	if report.ArticlesReviewed != 1 || report.PublishCount != 1 || report.RejectCount != 0 {
		t.Errorf("report = %+v, want ArticlesReviewed=1 PublishCount=1 RejectCount=0", report)
	}
	if report.Error != "" {
		t.Errorf("report.Error = %q, want empty on a successful run", report.Error)
	}
	if !strings.Contains(out, report.MarkdownSummary()) {
		t.Errorf("output = %q, want it to also print the report's markdown summary", out)
	}
}

// TestEditorialCycleCmd_ForcePromotesWinnerWithoutPrompt exercises the
// full training -> propose-winner -> confirm-and-promote continuation
// under --force: with a gate-passing winner available, the promotion
// happens automatically, with no prompt to answer (stdin is empty; the
// review loop itself has zero pending candidates since every seeded
// article already carries a human label — see
// seedTenPublishTenRejectReviewCandidates).
func TestEditorialCycleCmd_ForcePromotesWinnerWithoutPrompt(t *testing.T) {
	configPath, _ := seedTenPublishTenRejectReviewCandidates(t)

	out, err := testsupport.ExecuteWithStdin(t, "", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--force")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "promoted") {
		t.Errorf("output = %q, want the winning candidate auto-promoted under --force", out)
	}
}

// TestEditorialCycleCmd_PromotionDeclined_DoesNotPromote is the mirror of
// the --force test above: without --force, a winner is proposed and the
// human is prompted, and answering "n" must leave it unpromoted.
func TestEditorialCycleCmd_PromotionDeclined_DoesNotPromote(t *testing.T) {
	configPath, _ := seedTenPublishTenRejectReviewCandidates(t)

	out, err := testsupport.ExecuteWithStdin(t, "n\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "Promote this candidate") {
		t.Errorf("output = %q, want the promotion confirmation prompt shown", out)
	}
	if strings.Contains(out, "promoted ") {
		t.Errorf("output = %q, want no promotion recorded when the human declines", out)
	}
}

// TestEditorialCycle_DryRun_OpensReadOnly proves --dry-run --skip-sync
// leaves the database byte-for-byte (row-for-row) unchanged across every
// table, not just the specific rows other dry-run tests happen to check
// (design spec §13.2).
func TestEditorialCycle_DryRun_OpensReadOnly(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "ro1")

	before := dumpRowCounts(t, dbPath)

	if _, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--dry-run"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	after := dumpRowCounts(t, dbPath)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("row counts changed across --dry-run:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

// TestEditorialCycle_DryRun_SkipSync_StaleSchema_PrintsPendingMigrationsWarningBeforeSchemaError
// pins the fix for Important finding #2: on a database whose schema
// predates this branch's 0024/0025 migrations, BuildPlan's
// GetLatestResumable fails with a raw "no such column:
// total_candidates" SQL error, because --dry-run --skip-sync opens the
// database read-only and therefore never applies pending migrations
// (see the read-only branch of RunE). Before this fix, the
// pending-migrations warning this command already computes was printed
// only after BuildPlan returned, so a run against a database this stale
// never got to see it — it hit the cryptic SQL error instead. The
// warning must now print before BuildPlan runs, so it survives even
// though BuildPlan itself then fails.
func TestEditorialCycle_DryRun_SkipSync_StaleSchema_PrintsPendingMigrationsWarningBeforeSchemaError(t *testing.T) {
	configPath := testsupport.WriteFreshFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedPreEditorialCycleLifecycleSchema(t, dbPath)

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--dry-run")
	if err == nil {
		t.Fatal("Execute() error = nil, want BuildPlan's stale-schema failure to surface")
	}
	if !strings.Contains(out, "pending migration(s) not applied in read-only mode") {
		t.Errorf("output = %q, want the pending-migrations warning printed even though BuildPlan then failed", out)
	}
	if !strings.Contains(out, "0024_editorial_cycle_session_lifecycle.sql") || !strings.Contains(out, "0025_editorial_cycle_session_total_candidates.sql") {
		t.Errorf("output = %q, want both pending migrations named", out)
	}
}

// TestEditorialCycle_DryRun_ReportsExistingPausedSessionAccurately pauses
// a real session with one of three candidates already decided, then
// dry-runs, and pins that the plan lists exactly the frozen set's
// still-pending members — not a fresh, unrelated activelearning.Select
// preview (design spec §13.2).
func TestEditorialCycle_DryRun_ReportsExistingPausedSessionAccurately(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "pr1")
	seedReviewCandidate(t, dbPath, "pr2")
	seedReviewCandidate(t, dbPath, "pr3")

	firstOut, err := testsupport.ExecuteWithStdin(t, "p\nw\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy")
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	sessionID := extractStartedSessionID(t, firstOut)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
	decisions, err := reviewDecisions.ListBySession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("ListBySession() = %+v, want exactly one decision after save-and-exit", decisions)
	}
	decidedID := decisions[0].ArticleID
	db.Close()

	var pendingIDs []string
	for _, id := range []string{"pr1", "pr2", "pr3"} {
		if id != decidedID {
			pendingIDs = append(pendingIDs, id)
		}
	}

	before := dumpRowCounts(t, dbPath)
	dryOut, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run Execute() error = %v", err)
	}
	after := dumpRowCounts(t, dbPath)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("row counts changed by --dry-run itself:\nbefore = %+v\nafter  = %+v", before, after)
	}

	if !strings.Contains(dryOut, fmt.Sprintf("would resume session %s", sessionID)) {
		t.Errorf("output = %q, want it to say it would resume session %s", dryOut, sessionID)
	}
	if !strings.Contains(dryOut, "dry run: 2 candidate(s) selected, no changes made") {
		t.Errorf("output = %q, want exactly the 2 still-pending candidates reported (the frozen set, not a fresh selection)", dryOut)
	}
	for _, id := range pendingIDs {
		if !strings.Contains(dryOut, id) {
			t.Errorf("output = %q, want it to list still-pending candidate %s", dryOut, id)
		}
	}
	if strings.Contains(dryOut, decidedID) {
		t.Errorf("output = %q, want the already-decided candidate %s NOT listed as pending", dryOut, decidedID)
	}
}

// TestEditorialCycle_AllDecisionsThenEOF_Completes decides every seeded
// candidate and lets stdin run out with nothing left pending, and pins
// the piece none of this file's other completion-shaped tests check
// directly: the session's persisted status is actually "completed" (a
// direct GetLatestCompleted read, not just the "review complete" string
// on stdout), with exactly one editorial_review_decisions row per
// candidate (design spec §13.2).
//
// TestEditorialCycle_AllReviewActionsAndControlFlow and
// TestEditorialCycle_AmbiguousAndUpdateActions_JournaledCorrectly already
// exercise "every candidate decided, loop ends naturally, review
// complete" — but neither reads editorial_cycle_sessions.status back via
// GetLatestCompleted, which is what this test exists to pin.
func TestEditorialCycle_AllDecisionsThenEOF_Completes(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "ad1")
	seedReviewCandidate(t, dbPath, "ad2")

	// Two "p" answers decide both seeded candidates; stdin then hits EOF
	// with nothing left pending, so the review loop ends naturally and
	// Complete's guard is satisfied.
	out, err := testsupport.ExecuteWithStdin(t, "p\np\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "review complete") {
		t.Errorf("output = %q, want it to confirm the review completed", out)
	}
	sessionID := extractStartedSessionID(t, out)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	session, err := sessions.GetLatestCompleted(context.Background())
	if err != nil {
		t.Fatalf("GetLatestCompleted() error = %v", err)
	}
	if session.ID != sessionID || session.Status != "completed" {
		t.Errorf("session = %+v, want ID=%q Status=%q", session, sessionID, "completed")
	}

	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
	decisions, err := reviewDecisions.ListBySession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("ListBySession() = %+v, want one decision row per seeded candidate (2)", decisions)
	}
}

// TestEditorialCycle_SIGINT_PausesAndClosesResources exercises the same
// code path a real SIGINT drives — RunE's derived reviewCtx being
// cancelled mid-loop (scanLine's ctx.Done() branch, design spec §8.1) —
// via a context a background goroutine cancels only after the first
// decision has actually landed (observed by polling the DB, never a
// time.Sleep guess). See the design spec's §13.2 signal note.
//
// This test does not go through the testsupport.ExecuteWithContext helper: that
// helper's stdin is a fixed string (strings.NewReader), which hits EOF
// immediately once exhausted rather than blocking — it cannot reproduce
// scanner.Scan() genuinely still in flight when ctx is cancelled, which
// is the exact scenario this test exists to exercise. An io.Pipe with its
// writer deliberately left open is what makes the second scanner.Scan()
// call block until cancellation, so root command is driven directly here,
// mirroring TestEditorialCycle_PanicDuringReview_RecoversAndMarksSessionFailed's
// existing manual-root command style.
func TestEditorialCycle_SIGINT_PausesAndClosesResources(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "sig1")
	seedReviewCandidate(t, dbPath, "sig2")

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pw.Close()
		_ = pr.Close()
	})

	var out bytes.Buffer
	cli.Root().SetOut(&out)
	cli.Root().SetErr(&out)
	cli.Root().SetIn(pr)
	cli.Root().SetArgs([]string{
		"--config", configPath, "editorial", "cycle",
		"--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// cobra's dispatch (Command.ExecuteC, via Find) only ever copies
	// root command's ctx field down onto a found subcommand's own ctx field
	// when that field is still nil: "if cmd.ctx == nil { cmd.ctx = c.ctx
	// }" (github.com/spf13/cobra command.go). root command's "editorial cycle"
	// subcommand is a package-level singleton reused by every test in
	// this file (see the other tests' comments on that), so if any
	// earlier test already ran it through a plain Execute() first (most
	// have), its ctx field is already non-nil — a later ExecuteContext(ctx)
	// call would then only overwrite root command's own ctx field, never the
	// already-cached subcommand one, so cmd.Context() inside RunE would
	// silently keep returning whatever earlier, unrelated test's context
	// was cached, and this test would hang forever waiting for an
	// interrupt that can never actually reach the review loop. Forcing
	// both root command's and the subcommand's ctx field to this test's own
	// ctx up front makes cobra's copy a no-op (it already holds the value
	// cobra would copy), guaranteeing cmd.Context() observes exactly this
	// ctx regardless of what any earlier test in this file did.
	cycleTarget, _, findErr := cli.Root().Find([]string{"editorial", "cycle"})
	if findErr != nil {
		t.Fatalf("Find(editorial cycle) error = %v", findErr)
	}
	cli.Root().SetContext(ctx)
	cycleTarget.SetContext(ctx)
	// Restore both to a plain, never-cancelled context afterwards.
	// Leaving either ctx field pointing at this test's (by then
	// cancelled) context would poison every later test in this file the
	// same way: their plain Execute() calls would silently inherit an
	// already-cancelled context, failing with "context canceled" for
	// reasons that have nothing to do with what they're testing.
	t.Cleanup(func() {
		cli.Root().SetContext(context.Background())
		cycleTarget.SetContext(context.Background())
	})

	done := make(chan error, 1)
	go func() { done <- cli.Root().ExecuteContext(ctx) }()

	// Write exactly one decision, then leave the writer open — the
	// review loop's next scanner.Scan() call then blocks on the pipe,
	// exactly like a real interactive terminal between keystrokes.
	if _, err := io.WriteString(pw, "p\n"); err != nil {
		t.Fatalf("write decision: %v", err)
	}

	// Poll the DB until the first decision has actually landed, rather
	// than sleeping a fixed guess.
	deadline := time.Now().Add(10 * time.Second)
	for {
		n := func() int {
			db, err := infrasqlite.Open(dbPath, 5000)
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			defer db.Close()
			var n int
			if err := db.QueryRow(`SELECT COUNT(1) FROM editorial_review_decisions`).Scan(&n); err != nil {
				t.Fatalf("count decisions: %v", err)
			}
			return n
		}()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first decision to land")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the interrupted run to return")
	}
	if runErr != nil {
		t.Fatalf("Execute() error = %v, want the interrupted run to return nil (paused, not failed)", runErr)
	}
	if !strings.Contains(out.String(), "interrupted — session paused") {
		t.Errorf("output = %q, want the interrupted-session message", out.String())
	}

	// A fresh open on the same path must succeed immediately, with no
	// SQLITE_BUSY/locked error — proving the interrupted run's
	// `defer db.Close()` actually ran before ExecuteContext returned.
	fresh, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db immediately after interrupt: %v, want no busy/locked error", err)
	}
	defer fresh.Close()

	sessions := sqliteadapter.NewEditorialCycleSessionRepository(fresh)
	session, err := sessions.GetLatestResumable(context.Background())
	if err != nil {
		t.Fatalf("GetLatestResumable() error = %v", err)
	}
	if session.Status != "paused" {
		t.Errorf("session.Status = %q, want %q", session.Status, "paused")
	}
}

// TestEditorialCycle_ExecuteContext_AlreadyCancelled_FailsCleanlyBeforeAnySession
// exercises testsupport.ExecuteWithContext directly (unlike
// TestEditorialCycle_SIGINT_PausesAndClosesResources above, which needs a
// live io.Pipe to reproduce a read genuinely in flight when cancelled and
// so drives root command manually) with the simplest possible input: a context
// that is already cancelled before ExecuteContext even runs.
//
// cmd.Context() is threaded through every database operation in RunE, not
// only the review loop's scanLine (design spec §8.1's ctx.Done() branch
// is reached only once a session already exists and the loop is
// genuinely blocked on stdin — see the SIGINT test above). An
// already-cancelled context therefore fails at the very first
// context-aware call — re-applying migrations, here — well before a
// session is ever created, rather than reaching the graceful
// "interrupted — session paused" path. This test pins that real,
// observed behavior (a clean wrapped-context error, no partial session
// left behind) and, incidentally, proves testsupport.ExecuteWithContext's ctx
// actually reaches RunE at all (a cancelled context that were silently
// ignored — the exact latent cobra bug the SIGINT test's comment
// documents — would instead let this run proceed normally and succeed).
func TestEditorialCycle_ExecuteContext_AlreadyCancelled_FailsCleanlyBeforeAnySession(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "ctx1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before ExecuteContext even starts running

	_, err := testsupport.ExecuteWithContext(t, ctx, "", "--config", configPath, "editorial", "cycle",
		"--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err == nil {
		t.Fatal("Execute() error = nil, want the already-cancelled context to fail the run (proves ctx actually reached RunE)")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error = %q, want it to name the cancelled context", err.Error())
	}

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	if _, err := sessions.GetLatestResumable(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetLatestResumable() error = %v, want a wrapped sql.ErrNoRows — no session should have been created before the cancelled context aborted the run", err)
	}
}

// TestEditorialCycle_Resume_Repeated_Idempotent pauses a session with one
// of three candidates decided, then calls --resume --dry-run twice in a
// row: both dry-run outputs must be byte-identical and dumpRowCounts must
// be unchanged across both calls (design spec §13.2).
func TestEditorialCycle_Resume_Repeated_Idempotent(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "ri1")
	seedReviewCandidate(t, dbPath, "ri2")
	seedReviewCandidate(t, dbPath, "ri3")

	if _, err := testsupport.ExecuteWithStdin(t, "p\nw\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy"); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	before := dumpRowCounts(t, dbPath)
	out1, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--resume", "--dry-run")
	if err != nil {
		t.Fatalf("first --resume --dry-run Execute() error = %v", err)
	}
	mid := dumpRowCounts(t, dbPath)
	out2, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--resume", "--dry-run")
	if err != nil {
		t.Fatalf("second --resume --dry-run Execute() error = %v", err)
	}
	after := dumpRowCounts(t, dbPath)

	if out1 != out2 {
		t.Errorf("dry-run output changed across repeated --resume --dry-run calls:\nfirst  = %q\nsecond = %q", out1, out2)
	}
	if !reflect.DeepEqual(before, mid) {
		t.Errorf("row counts changed by the first --resume --dry-run:\nbefore = %+v\nafter  = %+v", before, mid)
	}
	if !reflect.DeepEqual(mid, after) {
		t.Errorf("row counts changed by the second --resume --dry-run:\nbefore = %+v\nafter  = %+v", mid, after)
	}
}

// TestEditorialCycle_Resume_AlreadyCompleted_NoOps completes a session
// fully, then runs again (with a freshly seeded, different candidate) and
// pins that a NEW session is started rather than the completed one being
// reused — GetLatestResumable's exclusion of "completed" (Task 3) is what
// this pins at the CLI level (design spec §13.2).
func TestEditorialCycle_Resume_AlreadyCompleted_NoOps(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "ac1")

	out1, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train")
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if !strings.Contains(out1, "review complete") {
		t.Fatalf("output = %q, want the first run to complete", out1)
	}
	firstSessionID := extractStartedSessionID(t, out1)

	seedReviewCandidate(t, dbPath, "ac2")

	out2, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train", "--resume")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	secondSessionID := extractStartedSessionID(t, out2)
	if secondSessionID == firstSessionID {
		t.Fatalf("second run reused the completed session %s, want a new one started", firstSessionID)
	}
	if !strings.Contains(out2, "started session "+secondSessionID) {
		t.Errorf("output = %q, want a \"started session\" line (not \"resuming\") since the prior session was completed", out2)
	}
}

// TestEditorialCycle_Redecide_RepresentsAlreadyDecidedCandidates decides
// one of two candidates publish and saves-and-exits (leaving the session
// "paused", not "completed" — --redecide/--resume only ever apply to a
// still-resumable session), then resumes with --redecide and re-decides
// the first one reject; pins that exactly one editorial_review_decisions
// row survives for it (updated in place, not duplicated — design spec
// §13.2) and that its active label is "reject". Under the append-only
// revision-history model the prior "publish" label is superseded, not
// deleted, so human_labels correctly ends up with two revisions for rd1
// (superseded "publish", active "reject").
//
// --redecide re-presents the WHOLE frozen set (ListFrozen, score DESC —
// see editorial_cycle_session_candidate_repository.go), including
// already-decided candidates, not just ListPending's anti-join. Since
// both seeded candidates are otherwise identical, their relative score
// order is not assumed; the second run's stdin is built after reading
// the actual frozen-set order back from the DB, so "reject" always lands
// on rd1 regardless of which position it's presented in.
func TestEditorialCycle_Redecide_RepresentsAlreadyDecidedCandidates(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "rd1")
	seedReviewCandidate(t, dbPath, "rd2")

	// "p" decides rd1 (or rd2 — order isn't assumed here either); "w"
	// then saves-and-exits at the other one, still undecided, leaving the
	// session paused rather than completed.
	firstOut, err := testsupport.ExecuteWithStdin(t, "p\nw\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy")
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	sessionID := extractStartedSessionID(t, firstOut)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	candidates := sqliteadapter.NewEditorialCycleSessionCandidateRepository(db)
	frozen, err := candidates.ListFrozen(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListFrozen() error = %v", err)
	}
	db.Close()
	if len(frozen) != 2 {
		t.Fatalf("ListFrozen() = %+v, want 2 frozen candidates", frozen)
	}

	// Build the redecide run's stdin so "r" always lands on rd1: if it's
	// presented first, reject it then save-and-exit at rd2; if it's
	// presented second, skip past rd2 first (an arbitrary, harmless
	// action — this test doesn't assert on rd2) then reject rd1.
	redecideStdin := "r\nw\n"
	if frozen[0].ArticleID != "rd1" {
		redecideStdin = "k\nr\n"
	}
	if _, err := testsupport.ExecuteWithStdin(t, redecideStdin, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train", "--resume", "--redecide"); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}

	after, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer after.Close()

	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(after)
	decisions, err := reviewDecisions.ListBySession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}
	var rd1Decisions int
	var rd1Action string
	for _, d := range decisions {
		if d.ArticleID == "rd1" {
			rd1Decisions++
			rd1Action = d.Action
		}
	}
	if rd1Decisions != 1 || rd1Action != "reject" {
		t.Fatalf("ListBySession() = %+v, want exactly one decision row for rd1 with action=reject (updated, not duplicated)", decisions)
	}

	labels := sqliteadapter.NewHumanLabelRepository(after)
	active, hasActive, err := labels.GetActive(context.Background(), "rd1")
	if err != nil {
		t.Fatalf("GetActive() error = %v", err)
	}
	if !hasActive || active.Label != "reject" {
		t.Fatalf("GetActive(rd1) = (%+v, hasActive=%v), want an active label of \"reject\"", active, hasActive)
	}

	// Positively demonstrate history is preserved rather than destroyed:
	// the superseded "publish" revision and the new active "reject"
	// revision must both still be present.
	got, err := labels.ListByArticle(context.Background(), "rd1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByArticle(rd1) = %+v, want exactly two revisions (superseded \"publish\" then active \"reject\")", got)
	}
}

// TestEditorialCycle_CorpusDrift_PurgedFrozenCandidate_ExcludedNotBlocking
// pauses a 2-candidate session, deletes one of the frozen candidates'
// articles directly via SQL (real corpus drift, not a value asserted
// without the deletion actually happening), then resumes and decides the
// survivor: the session must still reach "completed" (design spec
// §13.2). It also takes a --dry-run of the same paused-and-drifted
// session (read-only, side-effect-free) to confirm BuildPlan's
// DriftWarning computation reaches stdout there; see
// TestEditorialCycle_Resume_DriftWarning_PrintedOnRealResume for the
// equivalent assertion on the real (non-dry-run) resume path.
func TestEditorialCycle_CorpusDrift_PurgedFrozenCandidate_ExcludedNotBlocking(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "cd1")
	seedReviewCandidate(t, dbPath, "cd2")

	firstOut, err := testsupport.ExecuteWithStdin(t, "w\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy")
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	sessionID := extractStartedSessionID(t, firstOut)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM articles WHERE id = ?`, "cd1"); err != nil {
		t.Fatalf("delete purged article: %v", err)
	}
	db.Close()

	dryOut, err := testsupport.Execute(t, "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run Execute() error = %v", err)
	}
	if !strings.Contains(dryOut, "warning: 1 of 2 originally-frozen candidate(s) are no longer in the corpus") {
		t.Errorf("dry-run output = %q, want the drift warning", dryOut)
	}

	secondOut, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train", "--resume")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if !strings.Contains(secondOut, "review complete") {
		t.Errorf("output = %q, want the session to reach completion once the surviving candidate is decided (the purged one must not block completion)", secondOut)
	}

	after, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer after.Close()
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(after)
	session, err := sessions.GetLatestCompleted(context.Background())
	if err != nil {
		t.Fatalf("GetLatestCompleted() error = %v", err)
	}
	if session.ID != sessionID || session.Status != "completed" {
		t.Errorf("session = %+v, want ID=%q Status=%q", session, sessionID, "completed")
	}
}

// TestEditorialCycle_Resume_DriftWarning_PrintedOnRealResume closes the
// gap TestEditorialCycle_CorpusDrift_PurgedFrozenCandidate_ExcludedNotBlocking's
// doc comment flagged: a REAL (non-dry-run) `editorial cycle --resume`
// must also tell the operator when some of the session's
// originally-frozen candidates are no longer in the corpus, not only
// --dry-run. It pauses a 2-candidate session, deletes one frozen
// candidate's article via raw SQL (real corpus drift), then resumes for
// real and asserts the exact same warning text BuildPlan's --dry-run path
// prints appears in the real resume's output too.
func TestEditorialCycle_Resume_DriftWarning_PrintedOnRealResume(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "dw1")
	seedReviewCandidate(t, dbPath, "dw2")

	firstOut, err := testsupport.ExecuteWithStdin(t, "w\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy")
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	sessionID := extractStartedSessionID(t, firstOut)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM articles WHERE id = ?`, "dw1"); err != nil {
		t.Fatalf("delete purged article: %v", err)
	}
	db.Close()

	secondOut, err := testsupport.ExecuteWithStdin(t, "p\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train", "--resume")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if !strings.Contains(secondOut, "resuming session "+sessionID) {
		t.Fatalf("output = %q, want a real resume of session %s", secondOut, sessionID)
	}
	if !strings.Contains(secondOut, "warning: 1 of 2 originally-frozen candidate(s) are no longer in the corpus") {
		t.Errorf("output = %q, want the drift warning on the real resume path (byte-identical to the --dry-run path's wording)", secondOut)
	}
	if !strings.Contains(secondOut, "review complete") {
		t.Errorf("output = %q, want the session to reach completion once the surviving candidate is decided", secondOut)
	}
}

// TestEditorialCycle_CorpusDrift_AllFrozenCandidatesPurged_Cancels pauses
// a 2-candidate session, deletes BOTH frozen candidates' articles
// directly via SQL, then resumes: the old session must transition
// straight to "cancelled" and a fresh session must start in its place —
// GetLatestResumable must return the new session, not the cancelled one
// (cancel-on-corpus-drift transition).
func TestEditorialCycle_CorpusDrift_AllFrozenCandidatesPurged_Cancels(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "ap1")
	seedReviewCandidate(t, dbPath, "ap2")

	firstOut, err := testsupport.ExecuteWithStdin(t, "w\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy")
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	oldSessionID := extractStartedSessionID(t, firstOut)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM articles WHERE id IN (?, ?)`, "ap1", "ap2"); err != nil {
		t.Fatalf("delete purged articles: %v", err)
	}
	db.Close()

	seedReviewCandidate(t, dbPath, "ap3")

	secondOut, err := testsupport.ExecuteWithStdin(t, "w\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--resume")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	newSessionID := extractStartedSessionID(t, secondOut)
	if newSessionID == oldSessionID {
		t.Fatalf("second run reused the old session %s, want a fresh one started after every frozen candidate was purged", oldSessionID)
	}
	if !strings.Contains(secondOut, "started session "+newSessionID) {
		t.Errorf("output = %q, want a \"started session\" line for the new session", secondOut)
	}

	after, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer after.Close()

	var status string
	if err := after.QueryRow(`SELECT status FROM editorial_cycle_sessions WHERE id = ?`, oldSessionID).Scan(&status); err != nil {
		t.Fatalf("query old session status: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("old session status = %q, want %q", status, "cancelled")
	}

	sessions := sqliteadapter.NewEditorialCycleSessionRepository(after)
	latest, err := sessions.GetLatestResumable(context.Background())
	if err != nil {
		t.Fatalf("GetLatestResumable() error = %v", err)
	}
	if latest.ID != newSessionID {
		t.Errorf("GetLatestResumable() = %+v, want the new session %q — the cancelled old one must be excluded", latest, newSessionID)
	}
}

// TestEditorialCycle_LegacySession_MissingFrozenSet_BackfillsWithWarning
// seeds a "paused" session row directly via SQL with zero matching
// editorial_cycle_session_candidates rows (simulating data from before
// migration 0024's table existed), seeds real candidate articles matching
// what activelearning.Select would pick given the row's flags_json ('{}'
// — falling back to legacyBackfillSampleSize/legacyBackfillWindowDays,
// see session.go), then resumes via the real CLI and pins that the
// one-time backfill actually ran: editorial_cycle_session_candidates now
// has rows for that session ID, total_candidates was stamped to match,
// and the operator sees the design spec's §11.3 warning that the
// membership of this reconstructed set may differ from the original run.
func TestEditorialCycle_LegacySession_MissingFrozenSet_BackfillsWithWarning(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "ls1")
	seedReviewCandidate(t, dbPath, "ls2")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES (?, ?, ?, 'paused', 'review', '{}')`,
		"legacy-session", now, now); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	db.Close()

	out, err := testsupport.ExecuteWithStdin(t, "w\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train", "--resume")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "resuming session legacy-session") {
		t.Errorf("output = %q, want the legacy session actually resumed (not a fresh one started)", out)
	}
	wantWarning := "legacy session legacy-session: no recorded candidate set — recomputed from the current corpus; membership may differ from the original run"
	if !strings.Contains(out, wantWarning) {
		t.Errorf("output = %q, want the legacy-backfill warning %q", out, wantWarning)
	}

	after, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer after.Close()

	candidates := sqliteadapter.NewEditorialCycleSessionCandidateRepository(after)
	frozen, err := candidates.ListFrozen(context.Background(), "legacy-session")
	if err != nil {
		t.Fatalf("ListFrozen() error = %v", err)
	}
	if len(frozen) == 0 {
		t.Fatalf("ListFrozen() = %+v, want the one-time legacy backfill to have populated a frozen set", frozen)
	}

	var totalCandidates int
	if err := after.QueryRow(`SELECT total_candidates FROM editorial_cycle_sessions WHERE id = ?`, "legacy-session").Scan(&totalCandidates); err != nil {
		t.Fatalf("query total_candidates: %v", err)
	}
	if totalCandidates != len(frozen) {
		t.Errorf("total_candidates = %d, want it to match the backfilled frozen set size %d", totalCandidates, len(frozen))
	}
}

func mustID(t *testing.T, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}

func TestEditorialCycleCmd_ExcludesSourceBypassesFromFreshAndResumedReview(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume_%t", resume), func(t *testing.T) {
			configPath := testsupport.WriteFixture(t)
			dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
			seedReviewCandidate(t, dbPath, "manual-article")
			db, err := infrasqlite.Open(dbPath, 5000)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			articles := sqliteadapter.NewArticleRepository(db)
			for _, id := range []string{"bypass-article", "unconditional-article"} {
				a, err := articles.FindByID(context.Background(), "manual-article")
				if err != nil {
					t.Fatal(err)
				}
				a.ID, a.Title = id, "OFFICIAL CONTENT "+id
				a.OriginalURL, a.CanonicalURL = "https://example.com/"+id, "https://example.com/"+id
				if id == "bypass-article" {
					a.Source.ID = mustID(t, source.TypeTwitter, "sprisotopes")
				} else {
					a.Source.ID = mustID(t, source.TypeWeb, "springfield-isotopes-official")
				}
				if err := articles.Save(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-dictionary", "--skip-train"}
			if resume {
				// Freeze the old session before configuring the source bypasses.
				if _, err := testsupport.ExecuteWithStdin(t, "w\n", args...); err != nil {
					t.Fatal(err)
				}
				// Exclusion must preserve existing label history.
				if err := sqliteadapter.NewHumanLabelRepository(db).Label(context.Background(), training.Example{
					ArticleID: "bypass-article", Label: "publish", LabeledAt: time.Now(),
				}); err != nil {
					t.Fatal(err)
				}
			}
			contents, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			configured := strings.Replace(string(contents), "editorial:\n", "editorial:\n  model_bypass_source_ids: [twitter:sprisotopes]\n  unconditional_publish_source_ids: [web:springfield-isotopes-official]\n", 1)
			if err := os.WriteFile(configPath, []byte(configured), 0o600); err != nil {
				t.Fatal(err)
			}
			before := dumpRowCounts(t, dbPath)
			preview, err := testsupport.ExecuteWithStdin(t, "", append(args, "--dry-run")...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(preview, "OFFICIAL CONTENT") || !strings.Contains(preview, "manual-article") {
				t.Fatalf("incorrect dry-run candidates: %s", preview)
			}
			if !reflect.DeepEqual(before, dumpRowCounts(t, dbPath)) {
				t.Fatal("dry run changed database rows")
			}
			output, err := testsupport.ExecuteWithStdin(t, "p\n", args...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output, "OFFICIAL CONTENT") || !strings.Contains(output, "manual-article") {
				t.Fatalf("incorrect review candidates: %s", output)
			}
			session, err := sqliteadapter.NewEditorialCycleSessionRepository(db).GetLatestCompleted(context.Background())
			if err != nil || session.Status != "completed" {
				t.Fatalf("session status = %q, error = %v, want completed", session.Status, err)
			}
			var labelCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM human_labels WHERE article_id IN ('bypass-article', 'unconditional-article')`).Scan(&labelCount); err != nil {
				t.Fatal(err)
			}
			wantLabels := 0
			if resume {
				wantLabels = 1
			}
			if labelCount != wantLabels {
				t.Fatalf("official label count = %d, want %d (preserve existing labels without inventing new ones)", labelCount, wantLabels)
			}
			if resume {
				decisions, err := sqliteadapter.NewReviewDecisionRepository(db).ListBySession(context.Background(), session.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(decisions) != 3 {
					t.Fatalf("decisions = %+v, want manual publish and two audited source-policy skips", decisions)
				}
			}
		})
	}
}

func TestEditorialCycleCmd_DictionaryQuitOrEOFStopsBeforeTrainingAndDeployment(t *testing.T) {
	for _, input := range []string{"p\nq\n", "p\na\nq\n", "p\n"} {
		t.Run(strings.ReplaceAll(input, "\n", "_"), func(t *testing.T) {
			configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
			dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
			seedReviewCandidate(t, dbPath, "dictionary-quit")
			db, err := infrasqlite.Open(dbPath, 5000)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec("UPDATE articles SET title = ? WHERE id = ?", "Ezo Quarrington leads training camp session", "dictionary-quit")
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(dictPath)
			if err != nil {
				t.Fatal(err)
			}
			// Neither training nor deployment is skipped: reaching either would fail
			// with this fixture. Buffered input must survive the review/dictionary boundary.
			installFakeRemoteOps(t, nil)
			output, err := testsupport.ExecuteWithStdin(t, input, "--config", configPath,
				"editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--remote", "unused@host")
			if err != nil {
				t.Fatalf("err=%v\n%s", err, output)
			}
			if !strings.Contains(output, "editorial cycle stopped") || strings.Contains(output, "ci=") || strings.Contains(output, "no candidate passed gates") {
				t.Fatalf("cycle did not stop after dictionary review:\n%s", output)
			}
			after, err := os.ReadFile(dictPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(input, "a\n") {
				if bytes.Equal(before, after) {
					t.Fatal("approved dictionary progress was not written")
				}
			} else if !bytes.Equal(before, after) {
				t.Fatal("dictionary changed without approval")
			}
		})
	}
}

func TestEditorialCycleCmd_NoRemoteOps_SyncStepErrors(t *testing.T) {
	shared.ClearRemoteOps()
	configPath := testsupport.WriteFixture(t)
	_, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-deploy", "--remote", "deploy@example", "--non-interactive")
	if !errors.Is(err, shared.ErrNoRemoteOps) {
		t.Fatalf("err = %v, want it to wrap shared.ErrNoRemoteOps", err)
	}
}

func TestEditorialCycleCmd_NoRemoteOps_DeployStepErrors(t *testing.T) {
	shared.ClearRemoteOps()
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedReviewCandidate(t, dbPath, "a1")
	_, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "editorial", "cycle", "--skip-telegram-import", "--skip-sync", "--remote", "deploy@example", "--non-interactive")
	if !errors.Is(err, shared.ErrNoRemoteOps) {
		t.Fatalf("err = %v, want it to wrap shared.ErrNoRemoteOps", err)
	}
}
