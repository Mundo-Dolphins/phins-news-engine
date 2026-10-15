package audit_test

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

func TestAuditDuplicatesCmd_ReportsRecentDedupRejections(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: "a1", Title: "Isotopes re-sign veteran lineman", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusRejectedDuplicate,
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	decisions := sqliteadapter.NewDecisionRepository(db)
	if err := decisions.Append(context.Background(), appout.Event{
		ArticleID: "a1", OccurredAt: time.Now(), EventType: "dedup", ReasonDetail: "near-duplicate of article a0 (similarity 0.97)",
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	out, err := testsupport.Execute(t, "--config", configPath, "audit", "duplicates", "--days", "7", "--limit", "50")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "Isotopes re-sign veteran lineman") {
		t.Errorf("output = %q, want it to mention the article's title", out)
	}
	if !strings.Contains(out, "near-duplicate of article a0 (similarity 0.97)") {
		t.Errorf("output = %q, want it to mention the decision's ReasonDetail", out)
	}
	if !strings.Contains(out, "1 total") {
		t.Errorf("output = %q, want it to report 1 total decision", out)
	}
}

func TestAuditQualityCmd_FlagsDecisionsBelowThreshold(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: "a2", Title: "Isotopes injury report ahead of Sunday", OriginalURL: "https://example.com/a2", CanonicalURL: "https://example.com/a2",
		FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusPublished,
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	decisions := sqliteadapter.NewDecisionRepository(db)
	if err := decisions.Append(context.Background(), appout.Event{
		ArticleID: "a2", OccurredAt: time.Now(), EventType: "editorial_shadow", Score: 40, Threshold: 60,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	out, err := testsupport.Execute(t, "--config", configPath, "audit", "quality", "--days", "7", "--limit", "50")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "[below threshold]") {
		t.Errorf("output = %q, want it to flag the decision as below threshold", out)
	}
	if !strings.Contains(out, "1 of 1 decisions were below their gating threshold") {
		t.Errorf("output = %q, want the below-threshold summary line", out)
	}
}
