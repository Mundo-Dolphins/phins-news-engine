package editorial_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// seedArticleForMetrics saves a minimal article row so article_processing_events'
// FK (article_id REFERENCES articles(id)) is satisfied before appending events
// for it — mirrors the pattern in decision_repository_test.go's mustSaveArticle.
func seedArticleForMetrics(t *testing.T, articles *sqliteadapter.ArticleRepository, id string) {
	t.Helper()
	a := article.Article{
		ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source:      source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt:   time.Now(),
		PublishedAt: time.Now(),
		Status:      article.StatusFetched,
	}
	if err := articles.Save(context.Background(), a); err != nil {
		t.Fatalf("seed article %s: %v", id, err)
	}
}

// seedEditorialEvents opens the fixture's real, migrated SQLite database,
// seeds an article row for every distinct ArticleID referenced by events
// (required by article_processing_events' FK), and appends each event via
// the real DecisionRepository.
func seedEditorialEvents(t *testing.T, configPath string, events []out.Event) {
	t.Helper()
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	articles := sqliteadapter.NewArticleRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	ctx := context.Background()

	seeded := map[string]bool{}
	for _, e := range events {
		if !seeded[e.ArticleID] {
			seedArticleForMetrics(t, articles, e.ArticleID)
			seeded[e.ArticleID] = true
		}
		if err := decisions.Append(ctx, e); err != nil {
			t.Fatalf("append event for %s: %v", e.ArticleID, err)
		}
	}
}

func TestEditorialMetrics_ReportsVolumeAndZoneRates(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	now := time.Now().UTC()
	seedEditorialEvents(t, configPath, []out.Event{
		{ArticleID: "a1", EventType: "editorial", ReasonCode: "editorial:reject_score", Mode: "enforce", Score: 5, OccurredAt: now},
		{ArticleID: "a2", EventType: "editorial", ReasonCode: "editorial:ambiguous_reject", Mode: "enforce", Score: 45, OccurredAt: now},
		{ArticleID: "a3", EventType: "editorial_shadow", Mode: "shadow", Score: 95, OccurredAt: now},
	})

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "metrics", "--since", "24h")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	for _, want := range []string{"reject_score", "ambiguous_reject", "total=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to contain %q", got, want)
		}
	}
}

func TestEditorialMetrics_JSONFlag_ProducesValidJSON(t *testing.T) {
	configPath := testsupport.WriteFixture(t)

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "metrics", "--since", "24h", "--json")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	var report struct {
		Since         time.Time      `json:"since"`
		Total         int            `json:"total"`
		ByMode        map[string]int `json:"by_mode"`
		ByReasonCode  map[string]int `json:"by_reason_code"`
		RejectRate    float64        `json:"reject_rate"`
		AmbiguousRate float64        `json:"ambiguous_rate"`
		ByScoreDecile [10]int        `json:"by_score_decile"`
	}
	if err := json.Unmarshal([]byte(got), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v (output: %s)", err, got)
	}
	if report.Total != 0 {
		t.Errorf("Total = %d, want 0 for an empty database", report.Total)
	}
}
