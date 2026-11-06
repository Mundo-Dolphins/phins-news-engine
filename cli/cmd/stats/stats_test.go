package stats_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func seedStatsArticle(t *testing.T, dbPath string, a article.Article) {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), a); err != nil {
		t.Fatalf("seed article %s: %v", a.ID, err)
	}
}

func TestStats_ReportsSourcesArticlesAndRejections(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	now := time.Now()

	feedA := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	feedB := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-b"), Name: "Feed B", Type: source.TypeRSS}

	seedStatsArticle(t, dbPath, article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source: feedA, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
	})
	seedStatsArticle(t, dbPath, article.Article{
		ID: "a2", OriginalURL: "https://example.com/a2", CanonicalURL: "https://example.com/a2",
		Source: feedA, FetchedAt: now, PublishedAt: now,
		Status: article.StatusRejectedQuality, StatusReason: "editorial score below threshold",
	})
	seedStatsArticle(t, dbPath, article.Article{
		ID: "a3", OriginalURL: "https://example.com/a3", CanonicalURL: "https://example.com/a3",
		Source: feedB, FetchedAt: now, PublishedAt: now,
		Status: article.StatusRejectedDuplicate, StatusReason: "near-duplicate of a1",
	})

	got, err := testsupport.Execute(t, "--config", configPath, "stats", "--since", "24h", "--log-path", filepath.Join(t.TempDir(), "missing.log"))
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	for _, want := range []string{
		"sources=2", "analyzed=3", "accepted=1", "rejected=2",
		"editorial score below threshold", "near-duplicate of a1",
		"Feed A", "Feed B",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to contain %q", got, want)
		}
	}
}

func TestStats_JSONFlag_ProducesValidJSON(t *testing.T) {
	configPath := testsupport.WriteFixture(t)

	got, err := testsupport.Execute(t, "--config", configPath, "stats", "--since", "24h", "--json", "--log-path", filepath.Join(t.TempDir(), "missing.log"))
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	var report struct {
		SourcesProcessed int `json:"sources_processed"`
		ArticlesAnalyzed int `json:"articles_analyzed"`
	}
	if err := json.Unmarshal([]byte(got), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v (output: %s)", err, got)
	}
	if report.ArticlesAnalyzed != 0 {
		t.Errorf("ArticlesAnalyzed = %d, want 0 for an empty database", report.ArticlesAnalyzed)
	}
}

func TestStats_LogPathDefaultsToHostSetting(t *testing.T) {
	t.Cleanup(func() { shared.StatsLogPath = shared.DefaultStatsLogPath })
	configPath := testsupport.WriteFixture(t)
	for _, tc := range []struct{ name, setting, want string }{
		{"library default", shared.DefaultStatsLogPath, "data/logs/out.log"},
		{"host override", "data/host-logs/out.log", "data/host-logs/out.log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared.StatsLogPath = tc.setting
			got, err := testsupport.Execute(t, "--config", configPath, "stats", "--since", "24h", "--json")
			if err != nil {
				t.Fatalf("Execute() error = %v, output: %s", err, got)
			}
			var report struct {
				LogPath string `json:"log_path"`
			}
			if err := json.Unmarshal([]byte(got), &report); err != nil {
				t.Fatalf("output is not valid JSON: %v (output: %s)", err, got)
			}
			if report.LogPath != tc.want {
				t.Errorf("log_path = %q, want %q", report.LogPath, tc.want)
			}
		})
	}
}

func TestStats_ScansLogFileForCycleActivity(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	now := time.Now().UTC()

	logPath := filepath.Join(t.TempDir(), "out.log")
	lines := []string{
		`{"level":"info","fetched":10,"time":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","message":"process cycle completed"}`,
		`{"level":"error","time":"` + now.Add(-time.Minute).Format(time.RFC3339) + `","message":"boom"}`,
		`{"level":"info","time":"` + now.Add(-72*time.Hour).Format(time.RFC3339) + `","message":"process cycle completed","fetched":999}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write log fixture: %v", err)
	}

	got, err := testsupport.Execute(t, "--config", configPath, "stats", "--since", "24h", "--json", "--log-path", logPath)
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	var report struct {
		LogCyclesRun  int `json:"log_cycles_run"`
		LogFetchedSum int `json:"log_fetched_sum"`
		LogErrorLines int `json:"log_error_lines"`
	}
	if err := json.Unmarshal([]byte(got), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v (output: %s)", err, got)
	}
	if report.LogCyclesRun != 1 || report.LogFetchedSum != 10 || report.LogErrorLines != 1 {
		t.Errorf("report = %+v, want cycles=1 fetched_sum=10 error_lines=1 (the 72h-old line must be excluded)", report)
	}
}

var update = flag.Bool("update", false, "rewrite the golden files")

var timestampRE = regexp.MustCompile(`\d{4}-\d\d-\d\dT[\d:.]+(Z|[+-]\d\d:\d\d)`)

// goldenStatsRun seeds a deterministic database and log, runs `stats` with
// extra flags and returns the output with wall-clock timestamps and the
// temp log path masked. The seed uses one rejection status and one reason so
// the text report's map iteration order cannot vary.
func goldenStatsRun(t *testing.T, extra ...string) string {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	now := time.Now()
	feedA := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	feedB := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-b"), Name: "Feed B", Type: source.TypeRSS}
	for _, a := range []article.Article{
		{ID: "a1", Source: feedA, Status: article.StatusPublished},
		{ID: "a2", Source: feedA, Status: article.StatusRejectedQuality, StatusReason: "editorial score below threshold"},
		{ID: "a3", Source: feedB, Status: article.StatusRejectedQuality, StatusReason: "editorial score below threshold"},
		{ID: "a4", Source: feedB, Status: article.StatusPublishing},
	} {
		a.OriginalURL = "https://example.com/" + a.ID
		a.CanonicalURL = a.OriginalURL
		a.FetchedAt, a.PublishedAt = now, now
		seedStatsArticle(t, dbPath, a)
	}
	logPath := filepath.Join(t.TempDir(), "out.log")
	lines := []string{
		`{"level":"info","fetched":7,"time":"` + now.Add(-time.Hour).UTC().Format(time.RFC3339) + `","message":"process cycle completed"}`,
		`{"level":"error","time":"` + now.Add(-time.Minute).UTC().Format(time.RFC3339) + `","message":"boom"}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	args := append([]string{"--config", configPath, "stats", "--since", "24h", "--log-path", logPath}, extra...)
	got, err := testsupport.Execute(t, args...)
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	got = strings.ReplaceAll(got, logPath, "<LOG>")
	got = timestampRE.ReplaceAllString(got, "<TS>")
	return regexp.MustCompile(`window=[\d.]+h`).ReplaceAllString(got, "window=<H>h")
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestStatsOutputUnchanged(t *testing.T) {
	checkGolden(t, "stats.txt", goldenStatsRun(t))
}

func TestStatsJSONUnchanged(t *testing.T) {
	checkGolden(t, "stats.json", goldenStatsRun(t, "--json"))
}
