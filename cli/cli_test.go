package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func TestConfigValidate_Success(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "operational.db")
	sourcesPath := filepath.Join(dir, "sources.yml")
	if err := os.WriteFile(sourcesPath, []byte(`
websites: []
rss_feeds:
  - name: "Feed"
    url: "https://example.com/feed.xml"
    interval_minutes: 5
    max_items_per_cycle: 20
    max_age_hours: 24
    enabled: true
reddit: {feeds: []}
bluesky: {accounts: [], feeds: []}
twitter: {proxy_base_url: "", accounts: [], searches: []}
instagram: {rss_base_url: "", accounts: []}
youtube: {rss_base_url: "", accounts: []}
`), 0o600); err != nil {
		t.Fatalf("write sources fixture: %v", err)
	}
	contents := strings.Replace(fmt.Sprintf(testsupport.FixtureTemplate, dbPath), "sources_path: configs/sources.yml", "sources_path: "+sourcesPath, 1)
	contents += "fetch_sources:\n  rss: {enabled: true}\n"
	path := filepath.Join(dir, "settings.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	out, err := testsupport.Execute(t, "config", "validate", "--config", path)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "config OK") {
		t.Errorf("output = %q, want it to contain %q", out, "config OK")
	}
}

func TestConfigValidate_MissingFile(t *testing.T) {
	_, err := testsupport.Execute(t, "config", "validate", "--config", filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing config file")
	}
}

func TestSQLiteInspect_Success(t *testing.T) {
	path := testsupport.WriteFixture(t)

	out, err := testsupport.Execute(t, "sqlite", "inspect", "--config", path)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "articles: 0") {
		t.Errorf("output = %q, want it to contain %q", out, "articles: 0")
	}
	if !strings.Contains(out, "sources: 0") {
		t.Errorf("output = %q, want it to contain %q", out, "sources: 0")
	}
}

func TestSQLiteMigrateStatus_FreshDatabaseShowsAllPending(t *testing.T) {
	path := testsupport.WriteFreshFixture(t)

	out, err := testsupport.Execute(t, "sqlite", "migrate", "status", "--config", path)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "0001_init.sql: pending") {
		t.Errorf("output = %q, want it to contain %q", out, "0001_init.sql: pending")
	}
}

func TestSQLiteMigrateStatus_AfterInspectShowsApplied(t *testing.T) {
	path := testsupport.WriteFreshFixture(t)

	if _, err := testsupport.Execute(t, "sqlite", "inspect", "--config", path); err != nil {
		t.Fatalf("Execute(inspect) error = %v", err)
	}

	out, err := testsupport.Execute(t, "sqlite", "migrate", "status", "--config", path)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "0001_init.sql: applied") {
		t.Errorf("output = %q, want it to contain %q", out, "0001_init.sql: applied")
	}
	if strings.Contains(out, "pending") {
		t.Errorf("output = %q, want no pending migrations after inspect applied them all", out)
	}
}

func TestSQLiteIntegrity_Success(t *testing.T) {
	path := testsupport.WriteFixture(t)

	out, err := testsupport.Execute(t, "sqlite", "integrity", "--config", path)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "integrity check: ok") {
		t.Errorf("output = %q, want it to contain %q", out, "integrity check: ok")
	}
}

func TestRoot_HasExpectedSubcommands(t *testing.T) {
	want := []string{"config", "sqlite", "audit", "editorial-review", "editorial", "label", "train", "validate-model", "dictionary", "entities", "events"}
	got := map[string]bool{}
	for _, c := range cli.Root().Commands() {
		got[c.Name()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("Root() missing subcommand %q", name)
		}
	}
}

func TestSQLiteMergeLocal_CarriesLabelsForwardFromPreviousDatabase(t *testing.T) {
	// The investigation sync (the remote sync script) swaps a production
	// snapshot in; production never writes labels, so without this merge
	// every locally-made label is lost to a dated backup.
	path := testsupport.WriteFixture(t)
	previous := filepath.Join(t.TempDir(), "previous.db")
	testsupport.CopyMigratedTestDB(t, previous)
	prevDB, err := infrasqlite.Open(previous, 5*time.Second)
	if err != nil {
		t.Fatalf("open previous: %v", err)
	}
	if _, err := prevDB.Exec(`INSERT INTO editorial_examples (article_id, label, labeled_at, fetched_at, active, source)
		VALUES ('a1', 'reject', '2026-09-29T10:00:00Z', '2026-09-29T09:00:00Z', 1, 'interactive_review')`); err != nil {
		t.Fatalf("seed previous: %v", err)
	}
	if _, err := prevDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint previous: %v", err)
	}
	prevDB.Close()

	out, err := testsupport.Execute(t, "sqlite", "merge-local", "--from", previous, "--config", path)
	if err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}

	db, err := infrasqlite.Open(filepath.Join(filepath.Dir(path), "operational.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("open merged: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM editorial_examples WHERE article_id = 'a1' AND label = 'reject'`).Scan(&n); err != nil {
		t.Fatalf("query merged: %v", err)
	}
	if n != 1 {
		t.Errorf("merged editorial_examples rows for a1 = %d, want 1", n)
	}
}

func TestSQLiteMergeLocal_MissingPreviousFails(t *testing.T) {
	path := testsupport.WriteFixture(t)
	if _, err := testsupport.Execute(t, "sqlite", "merge-local", "--from", filepath.Join(t.TempDir(), "missing.db"), "--config", path); err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing previous database")
	}
}

func TestSQLiteMergeLocal_DBFlagTargetsGivenDatabase(t *testing.T) {
	path := testsupport.WriteFixture(t)
	target := filepath.Join(t.TempDir(), "other.db")
	testsupport.CopyMigratedTestDB(t, target)
	previous := filepath.Join(t.TempDir(), "previous.db")
	testsupport.CopyMigratedTestDB(t, previous)
	prevDB, err := infrasqlite.Open(previous, 5*time.Second)
	if err != nil {
		t.Fatalf("open previous: %v", err)
	}
	if _, err := prevDB.Exec(`INSERT INTO editorial_examples (article_id, label, labeled_at, fetched_at, active, source)
		VALUES ('a2', 'publish', '2026-09-29T10:00:00Z', '2026-09-29T09:00:00Z', 1, 'cli:label')`); err != nil {
		t.Fatalf("seed previous: %v", err)
	}
	prevDB.Close()

	if out, err := testsupport.Execute(t, "sqlite", "merge-local", "--from", previous, "--db", target, "--config", path); err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}
	count := func(dbPath string) int {
		db, err := infrasqlite.Open(dbPath, 5*time.Second)
		if err != nil {
			t.Fatalf("open %s: %v", dbPath, err)
		}
		defer db.Close()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM editorial_examples WHERE article_id = 'a2'`).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", dbPath, err)
		}
		return n
	}
	if got := count(target); got != 1 {
		t.Errorf("--db target rows = %d, want 1", got)
	}
	if got := count(filepath.Join(filepath.Dir(path), "operational.db")); got != 0 {
		t.Errorf("config database rows = %d, want 0 (untouched when --db is given)", got)
	}
}

func TestSQLiteMergeLocal_SummarizesPurgedArticleReferences(t *testing.T) {
	// Labels routinely outlive their article row (retention); one warning
	// line per row would bury the output, so they are counted per table.
	path := testsupport.WriteFixture(t)
	previous := filepath.Join(t.TempDir(), "previous.db")
	testsupport.CopyMigratedTestDB(t, previous)
	prevDB, err := infrasqlite.Open(previous, 5*time.Second)
	if err != nil {
		t.Fatalf("open previous: %v", err)
	}
	if _, err := prevDB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable fks: %v", err)
	}
	for _, id := range []string{"gone-1", "gone-2"} {
		if _, err := prevDB.Exec(`INSERT INTO human_labels (article_id, label, labeled_at, revision, active, source) VALUES (?, 'publish', '2026-09-01T00:00:00Z', 1, 1, 'telegramimport')`, id); err != nil {
			t.Fatalf("seed human_labels: %v", err)
		}
	}
	prevDB.Close()

	out, err := testsupport.Execute(t, "sqlite", "merge-local", "--from", previous, "--config", path)
	if err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}
	if strings.Contains(out, "rowid") {
		t.Errorf("output lists individual rows, want a per-table summary: %s", out)
	}
	if !strings.Contains(out, "2 human_labels row(s) reference purged articles") {
		t.Errorf("output = %q, want a per-table count of purged-article references", out)
	}
}

// The production snapshot can lag behind the local schema (e.g. predate
// migration 0045). merge-local must migrate it before merging, or clearing
// a locally-owned table the snapshot does not have yet fails the sync.
func TestSQLiteMergeLocal_MigratesLaggingSnapshotBeforeMerging(t *testing.T) {
	path := testsupport.WriteFixture(t)
	target := filepath.Join(t.TempDir(), "snapshot.db")
	testsupport.CopyMigratedTestDB(t, target)
	previous := filepath.Join(t.TempDir(), "previous.db")
	testsupport.CopyMigratedTestDB(t, previous)

	targetDB, err := infrasqlite.Open(target, 5*time.Second)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	for _, stmt := range []string{
		`DROP TABLE event_rule_proposals`,
		`DELETE FROM schema_migrations WHERE version = '0045_event_rule_proposals.sql'`,
	} {
		if _, err := targetDB.Exec(stmt); err != nil {
			t.Fatalf("make target lag (%s): %v", stmt, err)
		}
	}
	targetDB.Close()

	if out, err := testsupport.Execute(t, "sqlite", "merge-local", "--from", previous, "--db", target, "--config", path); err != nil {
		t.Fatalf("Execute() error = %v, output = %s", err, out)
	}
}
