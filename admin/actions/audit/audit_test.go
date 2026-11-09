package audit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/audit"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func newDB(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	cfg, err := config.Load(testsupport.WriteFixture(t))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, cfg
}

func run(t *testing.T, db *sql.DB, cfg *config.Config, mode action.Mode, name, params string) (action.Result, error) {
	t.Helper()
	for _, a := range audit.Actions() {
		if a.Name != name {
			continue
		}
		p, err := action.Decode(a, json.RawMessage(params))
		if err != nil {
			return action.Result{}, err
		}
		return a.Run(context.Background(), action.Env{DB: db, Config: cfg, Mode: mode, Now: func() time.Time { return now }}, p)
	}
	t.Fatalf("no action %q", name)
	return action.Result{}, nil
}

func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), article.Article{
		ID: "a1", Title: "Isotopes re-sign veteran lineman", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	})
	if err != nil {
		t.Fatalf("seed article: %v", err)
	}
	decisions := sqliteadapter.NewDecisionRepository(db)
	for _, e := range []appout.Event{
		{ArticleID: "a1", OccurredAt: now.Add(-time.Hour), EventType: "dedup", ReasonDetail: "near-duplicate of a0"},
		{ArticleID: "a1", OccurredAt: now.Add(-time.Hour), EventType: "editorial_shadow", Score: 40, Threshold: 60},
		{ArticleID: "a1", OccurredAt: now.Add(-30 * 24 * time.Hour), EventType: "dedup", ReasonDetail: "too old"},
	} {
		if err := decisions.Append(context.Background(), e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestMetadata(t *testing.T) {
	as := audit.Actions()
	if len(as) != 2 {
		t.Fatalf("got %d actions, want 2", len(as))
	}
	var reg action.Registry
	reg.Add(as...)
	for _, a := range as {
		if a.Group != "audit" || a.Kind != action.KindRead || len(a.Modes) != 2 || a.LongRunning || a.Exclusive || a.Title == "" || a.Description == "" {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		props, _ := action.Schema(a.Params)["properties"].(map[string]any)
		for name, p := range props {
			if prop, _ := p.(map[string]any); prop["description"] == nil {
				t.Errorf("%s param %s has no help", a.Name, name)
			}
		}
	}
}

func TestDuplicatesWindow(t *testing.T) {
	db, cfg := newDB(t)
	seed(t, db)
	res, err := run(t, db, cfg, action.ModeProduction, "audit.duplicates", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0]["title"] != "Isotopes re-sign veteran lineman" || res.Rows[0]["detail"] != "near-duplicate of a0" {
		t.Errorf("rows = %+v", res.Rows)
	}
	if !strings.Contains(res.Summary, "1 total") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestQualitySummary(t *testing.T) {
	db, cfg := newDB(t)
	seed(t, db)
	res, err := run(t, db, cfg, action.ModeLocal, "audit.quality", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "1 of 1 decisions were below their gating threshold" {
		t.Errorf("summary = %q", res.Summary)
	}
	if len(res.Rows) != 1 || res.Rows[0]["below_threshold"] != true {
		t.Errorf("rows = %+v", res.Rows)
	}
}

func TestAuditDaysMustBePositive(t *testing.T) {
	db, cfg := newDB(t)
	for _, name := range []string{"audit.duplicates", "audit.quality"} {
		for _, params := range []string{`{"days":0}`, `{"days":-3}`, `{"limit":0}`} {
			_, err := run(t, db, cfg, action.ModeLocal, name, params)
			wantCode(t, err, action.CodeInvalidParams)
		}
	}
}
