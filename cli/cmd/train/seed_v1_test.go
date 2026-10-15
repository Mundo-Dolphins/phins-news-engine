package train

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func TestBuildV1SeedDataset(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "v1.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE articles_local (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	normalized_url TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	source_handle TEXT NOT NULL DEFAULT '',
	domain TEXT NOT NULL DEFAULT '',
	source_published_at_utc TEXT NOT NULL DEFAULT '',
	published_at_utc TEXT NOT NULL DEFAULT '',
	fetched_at_utc TEXT NOT NULL DEFAULT '',
	content_type TEXT NOT NULL DEFAULT '',
	is_trusted_source INTEGER NOT NULL DEFAULT 0,
	was_published INTEGER NOT NULL DEFAULT 0,
	telegram_message_id TEXT NOT NULL DEFAULT '',
	discard_reason TEXT NOT NULL DEFAULT '',
	discard_subreason TEXT NOT NULL DEFAULT ''
);
INSERT INTO articles_local (
	id, title, url, source, source_handle, domain, source_published_at_utc, fetched_at_utc,
	content_type, is_trusted_source, was_published, telegram_message_id
) VALUES (
	1, 'Isotopes sign a cornerback', 'https://example.com/a', 'website', 'Example Site', 'example.com',
	'2026-01-01T10:00:00Z', '2026-01-01T10:02:00Z', 'hard_news', 1, 1, '10'
);
INSERT INTO articles_local (
	id, title, url, source, source_handle, domain, fetched_at_utc,
	content_type, was_published, discard_reason, discard_subreason
) VALUES (
	2, 'Basketball rumor unrelated to Springfield', 'https://noise.example/b', 'twitter', '#isotopes', 'twitter.com',
	'2026-01-01T11:00:00Z', 'social_reaction', 0, 'not_isotopes', ''
);
INSERT INTO articles_local (
	id, title, url, source, source_handle, domain, fetched_at_utc,
	was_published, discard_reason, discard_subreason
) VALUES (
	3, 'Quota-limited but otherwise plausible', 'https://example.com/c', 'rss', 'Example', 'example.com',
	'2026-01-01T12:00:00Z', 0, 'source_policy', 'max_per_day:domain:example.com'
);
`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	telegramPath := filepath.Join(dir, "channel_export.json")
	if err := os.WriteFile(telegramPath, []byte(`[{"id":10,"message":"Isotopes sign a cornerback\nhttps://example.com/a"}]`), 0o600); err != nil {
		t.Fatalf("write telegram export: %v", err)
	}
	outPath := filepath.Join(dir, "dataset.yml")
	summary, err := buildV1SeedDataset(context.Background(), v1SeedOptions{DBPath: dbPath, TelegramExportPath: telegramPath, OutPath: outPath, TeamSlug: "isotopes"})
	if err != nil {
		t.Fatalf("buildV1SeedDataset() error = %v", err)
	}
	if summary.PublishExamples != 1 || summary.RejectExamples != 1 || summary.Examples != 2 {
		t.Fatalf("summary = %+v, want 1 publish, 1 reject, 2 total", summary)
	}

	dataset, err := editorialtrain.LoadDataset(outPath)
	if err != nil {
		t.Fatalf("LoadDataset() error = %v", err)
	}
	if got := len(dataset.Examples); got != 2 {
		t.Fatalf("dataset examples = %d, want 2", got)
	}
	if dataset.Examples[0].ArticleID != "v1-1" || dataset.Examples[0].Label != "publish" {
		t.Errorf("first example = %+v, want published v1-1", dataset.Examples[0])
	}
	if dataset.Examples[1].ArticleID != "v1-2" || dataset.Examples[1].Label != "reject" {
		t.Errorf("second example = %+v, want rejected v1-2", dataset.Examples[1])
	}

	// With the default slug the team-specific v1 reason no longer matches,
	// so only the published example remains.
	summary, err = buildV1SeedDataset(context.Background(), v1SeedOptions{DBPath: dbPath, TelegramExportPath: telegramPath, OutPath: filepath.Join(dir, "default.yml")})
	if err != nil {
		t.Fatalf("buildV1SeedDataset(default slug) error = %v", err)
	}
	if summary.PublishExamples != 1 || summary.RejectExamples != 0 {
		t.Fatalf("summary with the default slug = %+v, want 1 publish and 0 reject", summary)
	}
}
