package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

func seedLogArticle(t *testing.T, db *sql.DB, id, sourceRaw string, status article.Status, publishedAt time.Time) {
	t.Helper()
	a := article.Article{
		ID: id, Title: "Title " + id, Summary: "Summary " + id,
		OriginalURL: "https://example.com/o/" + id, CanonicalURL: "https://example.com/c/" + id,
		Source:    source.Source{ID: mustID(t, source.TypeRSS, sourceRaw), Type: source.TypeRSS},
		FetchedAt: publishedAt, PublishedAt: publishedAt, Status: status,
	}
	if err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), a); err != nil {
		t.Fatalf("save article %s: %v", id, err)
	}
}

func seedOutboxRow(t *testing.T, db *sql.DB, articleID string, dest publishing.Destination, status string, sentAt time.Time) {
	t.Helper()
	var sent any
	if !sentAt.IsZero() {
		sent = sentAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := db.Exec(`INSERT INTO publication_outbox (article_id, destination, status, available_at, sent_at) VALUES (?, ?, ?, ?, ?)`,
		articleID, string(dest), status, "2020-01-01T00:00:00Z", sent); err != nil {
		t.Fatalf("seed outbox %s: %v", articleID, err)
	}
}

func TestPublicationLog_DeliveredBetween(t *testing.T) {
	db := newTestDB(t)
	from := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	sec := time.Second
	rows := []struct {
		id   string
		dest publishing.Destination
		st   string
		at   time.Time
	}{
		{"before", publishing.DestinationTelegram, "delivered", from.Add(-sec)},
		{"at-from", publishing.DestinationTelegram, "delivered", from},
		{"late", publishing.DestinationTelegram, "delivered", to.Add(-sec)},
		{"at-to", publishing.DestinationTelegram, "delivered", to},
		{"other-dest", publishing.DestinationBluesky, "delivered", from.Add(time.Minute)},
		{"not-delivered", publishing.DestinationTelegram, "pending", from.Add(time.Minute)},
	}
	for _, r := range rows {
		seedLogArticle(t, db, r.id, "src-a", article.StatusPublished, from)
		seedOutboxRow(t, db, r.id, r.dest, r.st, r.at)
	}
	// Empty canonical URL falls back to the original one.
	if _, err := db.Exec(`UPDATE articles SET canonical_url = '' WHERE id = 'late'`); err != nil {
		t.Fatal(err)
	}

	got, err := sqliteadapter.NewOutboxRepository(db).DeliveredBetween(context.Background(), publishing.DestinationTelegram, from, to)
	if err != nil {
		t.Fatalf("DeliveredBetween() error = %v", err)
	}
	if len(got) != 2 || got[0].ArticleID != "at-from" || got[1].ArticleID != "late" {
		t.Fatalf("DeliveredBetween() = %+v, want at-from then late", got)
	}
	if got[0].Title != "Title at-from" || got[0].Summary != "Summary at-from" || got[0].URL != "https://example.com/c/at-from" ||
		got[0].SourceID != mustID(t, source.TypeRSS, "src-a").String() || got[0].Destination != publishing.DestinationTelegram || !got[0].SentAt.Equal(from) {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].URL != "https://example.com/o/late" {
		t.Errorf("URL = %q, want original URL fallback", got[1].URL)
	}
}

func TestArticleLog_ArticlesBetween(t *testing.T) {
	db := newTestDB(t)
	from := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	sec := time.Second
	seedLogArticle(t, db, "before", "src-a", article.StatusFetched, from.Add(-sec))
	seedLogArticle(t, db, "b-late", "src-a", article.StatusRejectedQuality, to.Add(-sec))
	seedLogArticle(t, db, "a-first", "src-a", article.StatusPublished, from)
	seedLogArticle(t, db, "held", "src-b", article.StatusRetainedNoPublish, from.Add(time.Minute))
	seedLogArticle(t, db, "other-src", "src-c", article.StatusPublished, from.Add(time.Minute))
	seedLogArticle(t, db, "at-to", "src-a", article.StatusPublished, to)

	log := sqliteadapter.NewArticleLog(db)
	srcA, srcB := mustID(t, source.TypeRSS, "src-a").String(), mustID(t, source.TypeRSS, "src-b").String()
	got, err := log.ArticlesBetween(context.Background(), []string{srcA, srcB}, from, to)
	if err != nil {
		t.Fatalf("ArticlesBetween() error = %v", err)
	}
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	if len(ids) != 3 || ids[0] != "a-first" || ids[1] != "held" || ids[2] != "b-late" {
		t.Fatalf("ids = %v, want [a-first held b-late]", ids)
	}
	if got[0].Title != "Title a-first" || got[0].URL != "https://example.com/c/a-first" || got[0].SourceID != srcA || !got[0].PublishedAt.Equal(from) {
		t.Errorf("got[0] = %+v", got[0])
	}

	empty, err := log.ArticlesBetween(context.Background(), nil, from, to)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty sources = %v, %v, want empty and no error", empty, err)
	}
}
