package sqliteactions_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	sqliteactions "github.com/Mundo-Dolphins/phins-news-engine/admin/actions/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func run(t *testing.T, name string, migrate bool) (action.Result, error) {
	res, _, _, err := runDB(t, name, migrate)
	return res, err
}

func runDB(t *testing.T, name string, migrate bool) (action.Result, *sql.DB, *config.Config, error) {
	t.Helper()
	cfg, err := config.Load(testsupport.WriteFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var db *sql.DB
	if db, err = infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if migrate {
		if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range sqliteactions.Actions() {
		if a.Name != name {
			continue
		}
		p, err := action.Decode(a, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		env := action.Env{DB: db, Config: cfg, Mode: action.ModeProduction, Now: time.Now,
			Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Actor: "mcp"}
		res, err := a.Run(context.Background(), env, p)
		return res, db, cfg, err
	}
	t.Fatalf("action %s not found", name)
	return action.Result{}, nil, nil, nil
}

func TestInspectAction(t *testing.T) {
	res, _, cfg, err := runDB(t, "sqlite.inspect", true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res.Object)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["database"] != cfg.SQLite.Path {
		t.Errorf("database = %v, want %v", got["database"], cfg.SQLite.Path)
	}
	if got["articles"] != float64(0) {
		t.Errorf("articles = %v, want 0", got["articles"])
	}
	for _, k := range []string{"database", "articles", "sources", "outbox_pending", "outbox_leased", "open_clusters"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q in %s", k, raw)
		}
	}
}

func TestMigrateStatusAllApplied(t *testing.T) {
	res, err := run(t, "sqlite.migrate_status", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "table" || len(res.Rows) == 0 {
		t.Fatalf("result = %+v", res)
	}
	for _, r := range res.Rows {
		if r["state"] != "applied" {
			t.Errorf("row %v not applied", r)
		}
	}
}

func TestMigrateStatusPendingWhenUnmigrated(t *testing.T) {
	res, db, _, err := runDB(t, "sqlite.migrate_status", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("no rows")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE name = 'schema_migrations'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("schema_migrations exists after read-only status (n=%d, err=%v)", n, err)
	}
	for _, r := range res.Rows {
		if r["state"] != "pending" {
			t.Errorf("row %v not pending", r)
		}
	}
}

func TestIntegrityOK(t *testing.T) {
	res, err := run(t, "sqlite.integrity", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "text" || res.Text != "integrity check: ok" {
		t.Fatalf("result = %+v", res)
	}
	for _, a := range sqliteactions.Actions() {
		if a.Name == "sqlite.integrity" && !a.LongRunning {
			t.Error("integrity must be LongRunning")
		}
	}
}
