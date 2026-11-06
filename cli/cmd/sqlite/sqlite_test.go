package sqlitecmd_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestInspectOutputUnchanged(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	feed := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	ctx := context.Background()
	articles := sqliteadapter.NewArticleRepository(db)
	for _, id := range []string{"a1", "a2"} {
		if err := articles.Save(ctx, article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source: feed, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqliteadapter.NewOutboxRepository(db).Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', ?, ?, 'open')`, now, now); err != nil {
		t.Fatal(err)
	}
	db.Close()

	got, err := testsupport.Execute(t, "--config", configPath, "sqlite", "inspect")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	got = strings.Replace(got, dbPath, "<DB>", 1)
	checkGolden(t, "inspect.txt", got)
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
