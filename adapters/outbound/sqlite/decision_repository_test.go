package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func mustSaveArticle(t *testing.T, articles *sqliteadapter.ArticleRepository, id string) {
	t.Helper()
	a := article.Article{
		ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source:    source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusFetched,
	}
	if err := articles.Save(context.Background(), a); err != nil {
		t.Fatalf("Save(article %s) error = %v", id, err)
	}
}

func TestDecisionRepository_AppendAndListByArticle(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewDecisionRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	first := appout.Event{ArticleID: "a1", OccurredAt: now, FromStatus: article.StatusFetched, ToStatus: article.StatusNormalized, EventType: "normalize", CycleID: "cycle-1"}
	second := appout.Event{ArticleID: "a1", OccurredAt: now.Add(time.Second), FromStatus: article.StatusNormalized, ToStatus: article.StatusPendingPublication, EventType: "freshness", Score: 1, CycleID: "cycle-1"}

	if err := repo.Append(ctx, first); err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if err := repo.Append(ctx, second); err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}

	got, err := repo.ListByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].EventType != "normalize" || got[1].EventType != "freshness" {
		t.Errorf("events out of order: %+v", got)
	}
	if got[1].ToStatus != article.StatusPendingPublication {
		t.Errorf("ToStatus = %v, want %v", got[1].ToStatus, article.StatusPendingPublication)
	}
}

func TestDecisionRepository_ListByArticle_OrdersByIDWhenOccurredAtTies(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewDecisionRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	sameInstant := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	first := appout.Event{ArticleID: "a1", OccurredAt: sameInstant, ToStatus: article.StatusFetched, EventType: "fetch"}
	second := appout.Event{ArticleID: "a1", OccurredAt: sameInstant, ToStatus: article.StatusNormalized, EventType: "normalize"}

	if err := repo.Append(ctx, first); err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if err := repo.Append(ctx, second); err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}

	got, err := repo.ListByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(got) != 2 || got[0].EventType != "fetch" || got[1].EventType != "normalize" {
		t.Errorf("events with tied occurred_at not returned in insertion order: %+v", got)
	}
}

func TestDecisionRepository_DeleteOlderThan(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewDecisionRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	now := time.Now()
	old := appout.Event{ArticleID: "a1", OccurredAt: now.Add(-48 * time.Hour), ToStatus: article.StatusFetched, EventType: "fetch"}
	recent := appout.Event{ArticleID: "a1", OccurredAt: now, ToStatus: article.StatusNormalized, EventType: "normalize"}
	if err := repo.Append(ctx, old); err != nil {
		t.Fatalf("Append(old) error = %v", err)
	}
	if err := repo.Append(ctx, recent); err != nil {
		t.Fatalf("Append(recent) error = %v", err)
	}

	deleted, err := repo.DeleteOlderThan(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteOlderThan() error = %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteOlderThan() = %d, want 1", deleted)
	}

	got, err := repo.ListByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(got) != 1 || got[0].EventType != "normalize" {
		t.Errorf("remaining events = %+v, want only 'normalize'", got)
	}
}

func TestDecisionRepository_Append_PersistsModeChecksumAndRejectThreshold(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewDecisionRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "art-1")

	event := appout.Event{
		ArticleID: "art-1", OccurredAt: time.Now().UTC(), FromStatus: article.StatusClusterCandidate,
		ToStatus: article.StatusRejectedQuality, EventType: "editorial", ReasonCode: "editorial:reject_score",
		ClassifierVersion: "naive_bayes-v2", Score: 12.5, Threshold: 80, RejectThreshold: 20,
		Mode: "enforce", ModelChecksum: "abc123", CycleID: "cycle-1",
	}
	if err := repo.Append(ctx, event); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	got, err := repo.ListByArticle(ctx, "art-1")
	if err != nil {
		t.Fatalf("ListByArticle() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByArticle() returned %d events, want 1", len(got))
	}
	if got[0].Mode != "enforce" || got[0].ModelChecksum != "abc123" || got[0].RejectThreshold != 20 {
		t.Errorf("ListByArticle()[0] = %+v, want Mode=enforce ModelChecksum=abc123 RejectThreshold=20", got[0])
	}
}

func TestDecisionRepository_ListByEventTypesSince(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDecisionRepository(db)
	articles := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent1 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	recent2 := time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	for _, e := range []appout.Event{
		{ArticleID: "a1", OccurredAt: old, EventType: "editorial_shadow"},
		{ArticleID: "a1", OccurredAt: recent1, EventType: "editorial_shadow"},
		{ArticleID: "a1", OccurredAt: recent2, EventType: "clustering"}, // different event type, must be excluded
	} {
		if err := repo.Append(ctx, e); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	got, err := repo.ListByEventTypesSince(ctx, []string{"editorial_shadow", "editorial"}, cutoff, 10)
	if err != nil {
		t.Fatalf("ListByEventTypesSince() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (only recent1 is both in-window and matching event type)", len(got))
	}
	if !got[0].OccurredAt.Equal(recent1) {
		t.Errorf("got[0].OccurredAt = %v, want %v", got[0].OccurredAt, recent1)
	}
}
