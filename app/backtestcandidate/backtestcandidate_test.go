package backtestcandidate

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/replayfixtures"
)

func TestRun_ClassifiesAllFourOutcomeQuadrantsAgainstCandidateDictionary(t *testing.T) {
	dir := t.TempDir()

	// Fixture timestamps are computed relative to time.Now() (rather than
	// hardcoded) so the test stays inside Config.Days's window regardless
	// of when it runs.
	base := time.Now().UTC().Add(-1 * time.Hour)

	dbPath := filepath.Join(dir, "operational.db")
	testSource := source.Source{ID: mustSourceID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	seedRealDB(t, dbPath, []article.Article{
		// agree_publish: already published in prod, and the candidate
		// dictionary would accept it too (Isotopes-relevant).
		{
			ID: "agree-publish", OriginalURL: "https://example.com/agree-publish", CanonicalURL: "https://example.com/agree-publish",
			Title: "Springfield Isotopes sign a guy", Source: testSource, FetchedAt: base, PublishedAt: base.Add(-time.Hour), Status: article.StatusPendingPublication,
		},
		// regression: already published in prod, but the candidate
		// dictionary carries no Isotopes-relevance evidence for it.
		{
			ID: "regression", OriginalURL: "https://example.com/regression", CanonicalURL: "https://example.com/regression",
			Title: "A story with no relevant terms", Source: testSource, FetchedAt: base.Add(5 * time.Second), PublishedAt: base.Add(5*time.Second - time.Hour), Status: article.StatusPendingPublication,
		},
		// new_pass: prod rejected it, but the candidate dictionary accepts it.
		{
			ID: "new-pass", OriginalURL: "https://example.com/new-pass", CanonicalURL: "https://example.com/new-pass",
			Title: "Springfield Isotopes injury report", Source: testSource, FetchedAt: base.Add(10 * time.Second), PublishedAt: base.Add(10*time.Second - time.Hour), Status: article.StatusRejectedNotRelated,
		},
		// agree_reject: neither prod nor the candidate has any reason to publish this.
		{
			ID: "agree-reject", OriginalURL: "https://example.com/agree-reject", CanonicalURL: "https://example.com/agree-reject",
			Title: "Another irrelevant story", Source: testSource, FetchedAt: base.Add(15 * time.Second), PublishedAt: base.Add(15*time.Second - time.Hour), Status: article.StatusRejectedNotRelated,
		},
	})

	// cfg's own dictionary is irrelevant here — CandidateDictPath always
	// overrides it (see pipelinereplay.Overrides) — but config.Load still
	// needs a loadable path, so point it at the same fixture.
	candidateDictPath := replayfixtures.TestDictionaryYAML(t)
	cfgPath := replayfixtures.WriteTestConfig(t, candidateDictPath, "disabled", "")
	outPath := filepath.Join(dir, "report.csv")
	var progress []string

	report, err := Run(context.Background(), Config{
		Days: 30, DBPath: dbPath, CandidateDictPath: candidateDictPath, V2ConfigPath: cfgPath, OutPath: outPath,
		Progress: func(message string) {
			progress = append(progress, message)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if report.TotalArticles != 4 {
		t.Errorf("TotalArticles = %d, want 4", report.TotalArticles)
	}
	if report.Regressions != 1 {
		t.Errorf("Regressions = %d, want 1", report.Regressions)
	}
	if report.NewPasses != 1 {
		t.Errorf("NewPasses = %d, want 1", report.NewPasses)
	}
	if report.AgreePublish != 1 {
		t.Errorf("AgreePublish = %d, want 1", report.AgreePublish)
	}
	if report.AgreeReject != 1 {
		t.Errorf("AgreeReject = %d, want 1", report.AgreeReject)
	}

	byURL := make(map[string]Row, len(report.Rows))
	for _, r := range report.Rows {
		byURL[r.URL] = r
	}
	if got := byURL["https://example.com/regression"].Outcome; got != "regression" {
		t.Errorf("regression row Outcome = %q, want %q", got, "regression")
	}
	if got := byURL["https://example.com/new-pass"].Outcome; got != "new_pass" {
		t.Errorf("new-pass row Outcome = %q, want %q", got, "new_pass")
	}

	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open CSV: %v", err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read CSV: %v", err)
	}
	if len(records) != 5 { // header + 4 rows
		t.Fatalf("len(records) = %d, want 5", len(records))
	}
	if !containsProgress(progress, "loading v2 config") || !containsProgress(progress, "loaded 4 historical articles") || !containsProgress(progress, "done") {
		t.Fatalf("progress messages = %#v, want load/replay/done messages", progress)
	}
}

func TestRun_RequiresAtLeastOneCandidatePath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "operational.db")
	seedRealDB(t, dbPath, nil)
	cfgPath := replayfixtures.WriteTestConfig(t, replayfixtures.TestDictionaryYAML(t), "disabled", "")

	_, err := Run(context.Background(), Config{
		Days: 30, DBPath: dbPath, V2ConfigPath: cfgPath, OutPath: filepath.Join(dir, "report.csv"),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error (no candidate dictionary or model given)")
	}
}

func containsProgress(messages []string, want string) bool {
	for _, msg := range messages {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

// seedRealDB builds a real, fully-migrated v2 operational.db fixture and
// seeds it with articles via the real ArticleRepository.Save — exactly the
// shape backtestcandidate.Run reads via ArticleRepository.ListSince — then
// closes it, so Run reopens it fresh (read-only) the way it would a real
// operational.db.
func seedRealDB(t *testing.T, path string, articles []article.Article) {
	t.Helper()
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate fixture db: %v", err)
	}
	repo := sqliteadapter.NewArticleRepository(db)
	for _, a := range articles {
		if err := repo.Save(context.Background(), a); err != nil {
			t.Fatalf("seed article %s: %v", a.ID, err)
		}
	}
}

func mustSourceID(t *testing.T, platform source.Type, rawIdentifier string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, rawIdentifier)
	if err != nil {
		t.Fatalf("source.NewID(%q, %q) error = %v", platform, rawIdentifier, err)
	}
	return id
}
