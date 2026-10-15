package editorial_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestEditorialReviewCmd_ReportsRecentDecisions(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: "a1", Title: "Isotopes sign big extension", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusPendingPublication,
		Source: source.Source{ID: mustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	decisions := sqliteadapter.NewDecisionRepository(db)
	if err := decisions.Append(context.Background(), appout.Event{
		ArticleID: "a1", OccurredAt: time.Now(), EventType: "editorial_shadow", Score: 82.5,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	out, err := testsupport.Execute(t, "--config", configPath, "editorial-review", "--days", "7", "--limit", "50")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "editorial_shadow") {
		t.Errorf("output = %q, want it to mention editorial_shadow", out)
	}
	if !strings.Contains(out, "Isotopes sign big extension") {
		t.Errorf("output = %q, want it to mention the article's title", out)
	}
}

func TestEditorialReviewCmd_NoDecisionsPrintsZero(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	out, err := testsupport.Execute(t, "--config", configPath, "editorial-review")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "0 total") {
		t.Errorf("output = %q, want it to report 0 total decisions", out)
	}
}
