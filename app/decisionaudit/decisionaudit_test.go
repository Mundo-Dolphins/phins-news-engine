package decisionaudit_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/decisionaudit"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	decisions appout.DecisionRepository
	articles  appout.ArticleRepository
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "audit.db"), 5000)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The report must cope with a decision whose article row is gone, which
	// the foreign key would otherwise stop the test from creating; one
	// connection keeps the pragma in effect.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	return fixture{sqliteadapter.NewDecisionRepository(db), sqliteadapter.NewArticleRepository(db)}
}

func (f fixture) article(t *testing.T, id, title string, status article.Status) {
	t.Helper()
	id2, err := source.NewID(source.TypeRSS, "feed")
	if err != nil {
		t.Fatalf("source id: %v", err)
	}
	err = f.articles.Save(context.Background(), article.Article{
		ID: id, Title: title, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		FetchedAt: now, PublishedAt: now, Status: status,
		Source: source.Source{ID: id2, Name: "Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	})
	if err != nil {
		t.Fatalf("save article: %v", err)
	}
}

func (f fixture) event(t *testing.T, e appout.Event) {
	t.Helper()
	if err := f.decisions.Append(context.Background(), e); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestQualityFlagsBelowThreshold(t *testing.T) {
	f := newFixture(t)
	f.article(t, "low", "Low scorer", article.StatusPublished)
	f.article(t, "high", "High scorer", article.StatusPublished)
	f.event(t, appout.Event{ArticleID: "low", OccurredAt: now, EventType: "editorial_shadow", Score: 40, Threshold: 60})
	f.event(t, appout.Event{ArticleID: "high", OccurredAt: now.Add(-time.Minute), EventType: "editorial", Score: 80, Threshold: 60})
	f.event(t, appout.Event{ArticleID: "low", OccurredAt: now, EventType: "dedup"}) // not an editorial event

	rows, below, err := decisionaudit.Quality(context.Background(), f.decisions, f.articles, now.AddDate(0, 0, -7), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || below != 1 {
		t.Fatalf("rows=%d below=%d, want 2 and 1: %+v", len(rows), below, rows)
	}
	for _, r := range rows {
		if r.BelowThreshold != (r.ArticleID == "low") {
			t.Errorf("row %s BelowThreshold = %v", r.ArticleID, r.BelowThreshold)
		}
		if r.Status != string(article.StatusPublished) {
			t.Errorf("row %s status = %q", r.ArticleID, r.Status)
		}
	}
}

func TestDuplicatesMissingArticle(t *testing.T) {
	f := newFixture(t)
	f.event(t, appout.Event{ArticleID: "gone", OccurredAt: now, EventType: "dedup", ReasonDetail: "near-duplicate of a0"})

	rows, err := decisionaudit.Duplicates(context.Background(), f.decisions, f.articles, now.AddDate(0, 0, -7), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[0]; r.Title != "(article not found)" || r.Status != "(unknown)" || r.Detail != "near-duplicate of a0" {
		t.Errorf("row = %+v", r)
	}
}
