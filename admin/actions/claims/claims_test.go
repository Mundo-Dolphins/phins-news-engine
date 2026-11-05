package claims_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/claims"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t   *testing.T
	db  *sql.DB
	log *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return fixture{t: t, db: db, log: &bytes.Buffer{}}
}

// seed stores a claim with one outbox row per status (telegram, then bluesky).
func (f fixture) seed(key, state, articleID string, createdAt time.Time, statuses ...string) {
	f.t.Helper()
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(f.t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
		ID: articleID, OriginalURL: "https://example.com/" + articleID, CanonicalURL: "https://example.com/" + articleID,
		Source: src, FetchedAt: createdAt, PublishedAt: createdAt, Status: article.StatusPublished,
	}); err != nil {
		f.t.Fatalf("seed article: %v", err)
	}
	repo := sqliteadapter.NewEventClaimRepository(f.db)
	if ok, err := repo.TryClaim(ctx, "injury", key, articleID, json.RawMessage(`{"week":3}`), createdAt); err != nil || !ok {
		f.t.Fatalf("TryClaim = %v, %v", ok, err)
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE event_claims SET state = ? WHERE claim_key = ?`, state, key); err != nil {
		f.t.Fatalf("set state: %v", err)
	}
	for i, status := range statuses {
		if _, err := f.db.ExecContext(ctx, `
			INSERT INTO publication_outbox (article_id, destination, payload, status, available_at, attempt_count, last_error, expires_at)
			VALUES (?, ?, '{}', ?, ?, 6, ?, ?)`,
			articleID, []string{"telegram", "bluesky"}[i], status, createdAt, "boom "+status, createdAt); err != nil {
			f.t.Fatalf("seed outbox: %v", err)
		}
	}
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range claims.Actions() {
		if c.Name == name {
			a = c
		}
	}
	if a.Name == "" {
		f.t.Fatalf("no action %q", name)
	}
	p, err := action.Decode(a, json.RawMessage(params))
	if err != nil {
		return action.Result{}, err
	}
	env := action.Env{DB: f.db, Mode: action.ModeProduction, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: "user:ana"}
	return a.Run(context.Background(), env, p)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestMetadata(t *testing.T) {
	want := map[string]action.Kind{"claims.list": action.KindRead, "claims.show": action.KindRead,
		"claims.retry": action.KindWrite, "claims.release": action.KindDestructive}
	as := claims.Actions()
	if len(as) != 4 {
		t.Fatalf("%d actions", len(as))
	}
	for _, a := range as {
		if a.Group != "claims" || a.Kind != want[a.Name] || len(a.Modes) != 2 || a.Title == "" || a.Description == "" {
			t.Errorf("%+v", a)
		}
	}
	var reg action.Registry
	reg.Add(as...)
}

func TestList(t *testing.T) {
	f := newFixture(t)
	f.seed("old", "claimed", "a1", now.Add(-48*time.Hour))
	f.seed("new", "failed", "a2", now.Add(-time.Hour), "delivered", "dead")

	res, err := f.run("claims.list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "table" || res.Summary != "2 claim(s)" || len(res.Columns) != 7 || res.Columns[5] != (action.Column{Key: "last_error", Label: "Last error"}) {
		t.Fatalf("res = %+v", res)
	}
	byKey := map[any]map[string]any{}
	for _, r := range res.Rows {
		byKey[r["key"]] = r
	}
	n := byKey["new"]
	if n["handler"] != "injury" || n["state"] != "failed" || n["article"] != "a2" || n["outbox"] != "telegram:delivered,bluesky:dead" ||
		n["last_error"] != "boom dead" || n["created_at"] != now.Add(-time.Hour).Format(time.RFC3339) {
		t.Errorf("new = %v", n)
	}
	if o := byKey["old"]; o["outbox"] != "none" || o["last_error"] != "-" {
		t.Errorf("old = %v", o)
	}

	res, err = f.run("claims.list", `{"since":"24h","state":"failed","handler":"injury"}`)
	if err != nil || len(res.Rows) != 1 || res.Summary != "1 claim(s)" {
		t.Fatalf("filtered = %+v, %v", res, err)
	}
}

func TestListInvalidSince(t *testing.T) {
	_, err := newFixture(t).run("claims.list", `{"since":"yesterday"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestListInvalidState(t *testing.T) {
	_, err := newFixture(t).run("claims.list", `{"state":"bogus"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestShow(t *testing.T) {
	f := newFixture(t)
	f.seed("k1", "failed", "a1", now, "dead")
	res, err := f.run("claims.show", `{"handler":"injury","key":"k1"}`)
	if err != nil || res.Render != "object" {
		t.Fatalf("%+v, %v", res, err)
	}
	b, _ := json.Marshal(res.Object)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["handler"] != "injury" || got["key"] != "k1" || got["state"] != "failed" || got["article_id"] != "a1" ||
		got["created_at"] == nil || got["updated_at"] == nil || got["data"].(map[string]any)["week"] != float64(3) {
		t.Errorf("object = %s", b)
	}
	ob := got["outbox"].([]any)[0].(map[string]any)
	if ob["destination"] != "telegram" || ob["status"] != "dead" || ob["attempts"] != float64(6) || ob["last_error"] != "boom dead" {
		t.Errorf("outbox = %v", ob)
	}

	_, err = f.run("claims.show", `{"handler":"injury","key":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
	_, err = f.run("claims.show", `{"handler":"injury"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestRetry(t *testing.T) {
	tests := []struct {
		name    string
		missing bool
		state   string
		rows    []string
		code    string
		msg     string
	}{
		{name: "not found", missing: true, code: action.CodeNotFound, msg: "no claim injury/k1"},
		{name: "not failed", state: "claimed", rows: []string{"dead"}, code: action.CodeConflict, msg: "is not failed (only failed claims can be retried)"},
		{name: "in flight", state: "failed", rows: []string{"dead", "pending"}, code: action.CodeConflict, msg: "still has an in-flight outbox row"},
		{name: "nothing to retry", state: "failed", rows: []string{"delivered"}, code: action.CodeConflict, msg: "has no dead outbox row to retry (release it instead)"},
	}
	for _, tt := range tests {
		for _, dry := range []string{"false", "true"} {
			t.Run(tt.name+"/dry="+dry, func(t *testing.T) {
				f := newFixture(t)
				if !tt.missing {
					f.seed("k1", tt.state, "a1", now, tt.rows...)
				}
				_, err := f.run("claims.retry", `{"handler":"injury","key":"k1","dry_run":`+dry+`}`)
				wantCode(t, err, tt.code)
				if !strings.Contains(err.Error(), tt.msg) {
					t.Errorf("err = %q, want %q", err, tt.msg)
				}
			})
		}
	}

	t.Run("dry run", func(t *testing.T) {
		f := newFixture(t)
		f.seed("k1", "failed", "a1", now, "dead", "dead")
		res, err := f.run("claims.retry", `{"handler":"injury","key":"k1","dry_run":true}`)
		if err != nil || res.Render != "text" || res.Text != "dry run: would re-enqueue 2 outbox row(s) of injury/k1 and set the claim to claimed" {
			t.Fatalf("%+v, %v", res, err)
		}
		if v, _ := f.run("claims.show", `{"handler":"injury","key":"k1"}`); v.Object == nil {
			t.Fatal("claim vanished")
		}
		res, _ = f.run("claims.list", `{"state":"failed"}`)
		if len(res.Rows) != 1 {
			t.Error("dry run changed the claim")
		}
	})
	t.Run("retry", func(t *testing.T) {
		f := newFixture(t)
		f.seed("k1", "failed", "a1", now, "dead", "dead")
		res, err := f.run("claims.retry", `{"handler":"injury","key":"k1"}`)
		if err != nil || res.Render != "text" || res.Text != "retry injury/k1: 2 outbox row(s) re-enqueued, claim set to claimed" {
			t.Fatalf("%+v, %v", res, err)
		}
		list, _ := f.run("claims.list", `{"state":"claimed"}`)
		if len(list.Rows) != 1 || list.Rows[0]["outbox"] != "telegram:pending,bluesky:pending" {
			t.Errorf("after retry = %+v", list.Rows)
		}
	})
}

func TestRelease(t *testing.T) {
	f := newFixture(t)
	f.seed("k1", "failed", "a1", now, "dead")
	f.seed("pub", "published", "a2", now, "delivered")

	_, err := f.run("claims.release", `{"handler":"injury","key":"pub"}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "is published and would allow a duplicate post") {
		t.Errorf("err = %v", err)
	}
	_, err = f.run("claims.release", `{"handler":"injury","key":"nope"}`)
	wantCode(t, err, action.CodeNotFound)
	if f.log.Len() != 0 {
		t.Errorf("logged on failure: %s", f.log)
	}

	res, err := f.run("claims.release", `{"handler":"injury","key":"k1"}`)
	if err != nil || res.Render != "text" || res.Text != "released injury/k1 (by user:ana at 2026-11-05T12:00:00Z)" {
		t.Fatalf("%+v, %v", res, err)
	}
	for _, want := range []string{"event claim released", "handler=injury", "key=k1", "by=user:ana"} {
		if !strings.Contains(f.log.String(), want) {
			t.Errorf("log %q lacks %q", f.log, want)
		}
	}
	_, err = f.run("claims.show", `{"handler":"injury","key":"k1"}`)
	wantCode(t, err, action.CodeNotFound)
}
