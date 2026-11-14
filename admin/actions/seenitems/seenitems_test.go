package seenitemsactions_test

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
	seenitemsactions "github.com/Mundo-Dolphins/phins-news-engine/admin/actions/seenitems"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func backfillAction(t *testing.T) action.Action {
	t.Helper()
	as := seenitemsactions.Actions()
	if len(as) != 1 || as[0].Name != "seen_items.backfill" {
		t.Fatalf("Actions() = %+v, want only seen_items.backfill", as)
	}
	return as[0]
}

func TestBackfillMetadata(t *testing.T) {
	a := backfillAction(t)
	if a.Kind != action.KindWrite || !a.LongRunning || !a.Exclusive {
		t.Errorf("kind=%s long=%v exclusive=%v, want write/long/exclusive", a.Kind, a.LongRunning, a.Exclusive)
	}
	if len(a.Modes) != 2 || a.Modes[0] != action.ModeLocal || a.Modes[1] != action.ModeProduction {
		t.Errorf("modes = %v, want local and production", a.Modes)
	}
	if a.Group != "seen_items" {
		t.Errorf("group = %q", a.Group)
	}
}

func seededDB(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	cfg, err := config.Load(testsupport.WriteFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		id, ext, url, status string
		at                   time.Time
	}{
		{"a-old", "guid-1", "https://example.com/a", "rejected_not_related", now.Add(-48 * time.Hour)},
		{"a-new", "guid-1", "https://example.com/a", "published", now.Add(-time.Hour)},
		{"b", "", "https://example.com/b", "published", now.Add(-2 * time.Hour)},
	} {
		if _, err := db.Exec(`INSERT INTO articles (id, external_id, original_url, canonical_url, source_id, source_type, title, fetched_at, status)
			VALUES (?, ?, ?, ?, 'feed-a', 'rss', 't', ?, ?)`, r.id, r.ext, r.url, r.url, r.at, r.status); err != nil {
			t.Fatal(err)
		}
	}
	return db, cfg
}

func runBackfill(t *testing.T, db *sql.DB, cfg *config.Config) action.Result {
	t.Helper()
	a := backfillAction(t)
	p, err := action.Decode(a, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	env := action.Env{DB: db, Config: cfg, Mode: action.ModeProduction, Now: time.Now,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Actor: "mcp"}
	res, err := a.Run(context.Background(), env, p)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func fields(t *testing.T, res action.Result) map[string]any {
	t.Helper()
	if res.Render != "object" {
		t.Fatalf("render = %q, want object", res.Render)
	}
	raw, _ := json.Marshal(res.Object)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBackfillResultAndIdempotentSecondRun(t *testing.T) {
	db, cfg := seededDB(t)

	res := runBackfill(t, db, cfg)
	got := fields(t, res)
	if got["candidates"] != float64(2) || got["inserted"] != float64(2) || got["skipped"] != float64(0) {
		t.Fatalf("first run = %v, want candidates=2 inserted=2 skipped=0", got)
	}
	want := "seen_items backfill: 2 candidate identities, 2 inserted, 0 already present"
	if res.Summary != want {
		t.Errorf("summary = %q, want %q", res.Summary, want)
	}

	res = runBackfill(t, db, cfg)
	got = fields(t, res)
	if got["candidates"] != float64(2) || got["inserted"] != float64(0) || got["skipped"] != float64(2) {
		t.Fatalf("second run = %v, want inserted=0 skipped=2", got)
	}
	if want := "seen_items backfill: 2 candidate identities, 0 inserted, 2 already present"; res.Summary != want {
		t.Errorf("second summary = %q, want %q", res.Summary, want)
	}
}
