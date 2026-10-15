package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func seedSessionAndArticles(t *testing.T, db *sql.DB, sessionID string, articleIDs ...string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	if err := sessions.Create(ctx, out.EditorialCycleSession{ID: sessionID, StartedAt: now, UpdatedAt: now, Status: "running", CurrentStep: "review"}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	for _, id := range articleIDs {
		if err := articles.Save(ctx, article.Article{ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id, FetchedAt: now, PublishedAt: now, Status: article.StatusClustered}); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}
}

func TestReviewDecisionRepository_SaveAndListBySession(t *testing.T) {
	db := newTestDB(t)
	seedSessionAndArticles(t, db, "sess-1", "a1", "a2")
	repo := sqliteadapter.NewReviewDecisionRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Save(ctx, out.ReviewDecision{SessionID: "sess-1", ArticleID: "a1", Action: "publish", Label: "publish", DecidedAt: now}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := repo.Save(ctx, out.ReviewDecision{SessionID: "sess-1", ArticleID: "a2", Action: "ambiguous", DecidedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := repo.ListBySession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}
	if len(got) != 2 || got[0].ArticleID != "a1" || got[1].Action != "ambiguous" {
		t.Fatalf("ListBySession() = %+v, unexpected", got)
	}
}

func TestReviewDecisionRepository_ListRecentByAction_FiltersByActionAndSince(t *testing.T) {
	db := newTestDB(t)
	seedSessionAndArticles(t, db, "sess-1", "a1", "a2", "a3")
	repo := sqliteadapter.NewReviewDecisionRepository(db)
	ctx := context.Background()
	cutoff := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

	if err := repo.Save(ctx, out.ReviewDecision{SessionID: "sess-1", ArticleID: "a1", Action: "ambiguous", DecidedAt: cutoff.Add(time.Hour)}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := repo.Save(ctx, out.ReviewDecision{SessionID: "sess-1", ArticleID: "a2", Action: "ambiguous", DecidedAt: cutoff.Add(-time.Hour)}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := repo.Save(ctx, out.ReviewDecision{SessionID: "sess-1", ArticleID: "a3", Action: "publish", DecidedAt: cutoff.Add(time.Hour)}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := repo.ListRecentByAction(ctx, "ambiguous", cutoff)
	if err != nil {
		t.Fatalf("ListRecentByAction() error = %v", err)
	}
	if len(got) != 1 || got[0] != "a1" {
		t.Fatalf("ListRecentByAction() = %v, want [a1]", got)
	}
}
