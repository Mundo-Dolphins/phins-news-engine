package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func TestUnknownEntityRepository_SaveOccurrences(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewUnknownEntityRepository(db)
	ctx := context.Background()

	a := article.Article{ID: "a1", OriginalURL: "https://example.com/story", CanonicalURL: "https://example.com/story", FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusNormalized}
	if err := articles.Save(ctx, a); err != nil {
		t.Fatalf("Save(article) error = %v", err)
	}

	occurrences := []eventsignals.UnknownEntity{
		{Text: "Unknown Player", NormalizedText: "unknown player", Reason: "capitalized name-like span not found in entity catalog", Field: "title", Confidence: 0.5},
		{Text: "Second Candidate", NormalizedText: "second candidate", Reason: "capitalized name-like span not found in entity catalog", Field: "body", Confidence: 0.5},
	}
	if err := repo.SaveOccurrences(ctx, "a1", occurrences, "rules-v1", time.Now()); err != nil {
		t.Fatalf("SaveOccurrences() error = %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT article_id, candidate_text, normalized_text, possible_type, confidence, reason, field, rules_version FROM unknown_entity_occurrences ORDER BY candidate_text`)
	if err != nil {
		t.Fatalf("query unknown_entity_occurrences: %v", err)
	}
	defer rows.Close()

	type row struct {
		articleID, candidateText, normalizedText, possibleType, reason, field, rulesVersion string
		confidence                                                                          float64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.articleID, &r.candidateText, &r.normalizedText, &r.possibleType, &r.confidence, &r.reason, &r.field, &r.rulesVersion); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err(): %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].articleID != "a1" || got[0].candidateText != "Second Candidate" || got[0].normalizedText != "second candidate" || got[0].field != "body" || got[0].confidence != 0.5 || got[0].rulesVersion != "rules-v1" || got[0].possibleType != "" {
		t.Errorf("row[0] = %+v, unexpected values", got[0])
	}
	if got[1].candidateText != "Unknown Player" || got[1].field != "title" {
		t.Errorf("row[1] = %+v, unexpected values", got[1])
	}
}

func TestUnknownEntityRepository_SaveOccurrences_EmptyIsNoop(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewUnknownEntityRepository(db)
	ctx := context.Background()

	a := article.Article{ID: "a1", OriginalURL: "https://example.com/story", CanonicalURL: "https://example.com/story", FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusNormalized}
	if err := articles.Save(ctx, a); err != nil {
		t.Fatalf("Save(article) error = %v", err)
	}

	if err := repo.SaveOccurrences(ctx, "a1", nil, "rules-v1", time.Now()); err != nil {
		t.Fatalf("SaveOccurrences(nil) error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM unknown_entity_occurrences`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}
