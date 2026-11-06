package label_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/label"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

type fixture struct {
	t  *testing.T
	db *sql.DB
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	now := time.Now().UTC()
	if err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
	}); err != nil {
		t.Fatal(err)
	}
	return fixture{t, db}
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range label.Actions() {
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
	env := action.Env{DB: f.db, Mode: action.ModeProduction, Now: time.Now,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Actor: "user:javi"}
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
	want := map[string]action.Kind{"label.set": action.KindWrite, "label.resolve_conflict": action.KindWrite,
		"label.history": action.KindRead, "label.conflicts": action.KindRead}
	as := label.Actions()
	if len(as) != 4 {
		t.Fatalf("%d actions", len(as))
	}
	for _, a := range as {
		if a.Group != "label" || a.Kind != want[a.Name] || len(a.Modes) != 2 || a.Title == "" || a.Description == "" {
			t.Errorf("%+v", a)
		}
	}
	var reg action.Registry
	reg.Add(as...)
}

func TestLabelSetDefaultsByToActor(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run("label.set", `{"article_id":"a1","label":"publish"}`); err != nil {
		t.Fatal(err)
	}
	got, ok, err := sqliteadapter.NewHumanLabelRepository(f.db).GetActive(context.Background(), "a1")
	if err != nil || !ok {
		t.Fatalf("GetActive = %v, %v", ok, err)
	}
	if got.LabeledBy != "user:javi" || got.Source != "admin:label" {
		t.Errorf("by=%q source=%q", got.LabeledBy, got.Source)
	}
	res, err := f.run("label.history", `{"article_id":"a1"}`)
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("history = %+v, %v", res, err)
	}
}

func TestLabelSetConflictIs409(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(`INSERT INTO human_label_conflicts (table_name, article_id, detail, detected_at) VALUES ('human_labels', 'a1', 'x', ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("label.set", `{"article_id":"a1","label":"publish"}`)
	wantCode(t, err, action.CodeConflict)
	res, err := f.run("label.conflicts", `{}`)
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("conflicts = %+v, %v", res, err)
	}
	if _, err := f.run("label.resolve_conflict", `{"article_id":"a1","label":"reject"}`); err != nil {
		t.Fatal(err)
	}
}

func TestLabelSetRejectsBadLabel(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("label.set", `{"article_id":"a1","label":"maybe"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestLabelSetUnknownArticleIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("label.set", `{"article_id":"nope","label":"publish"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestResolveWithoutConflictIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("label.resolve_conflict", `{"article_id":"a1","label":"reject"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestLabelSetConflictMessageIsOperatorFacing(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(`INSERT INTO human_label_conflicts (table_name, article_id, detail, detected_at) VALUES ('human_labels', 'a1', 'x', ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("label.set", `{"article_id":"a1","label":"publish"}`)
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Message != "a1 has an unresolved legacy label conflict; use label.resolve_conflict" {
		t.Fatalf("err = %v", err)
	}
}
