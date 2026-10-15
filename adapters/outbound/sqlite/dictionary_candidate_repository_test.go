package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestDictionaryCandidateRepository_SaveIsIdempotentAndListOrdersBySupportCount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	if err := sessions.Create(ctx, out.EditorialCycleSession{ID: "sess-1", StartedAt: now, UpdatedAt: now, Status: "completed", CurrentStep: "review"}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(ctx, article.Article{ID: "a1", OriginalURL: "https://example.com/1", CanonicalURL: "https://example.com/1", FetchedAt: now, PublishedAt: now, Status: article.StatusPublished}); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	repo := sqliteadapter.NewDictionaryCandidateRepository(db)
	low := out.DictionaryCandidate{Term: "training camp", SessionID: "sess-1", SupportCount: 2, FirstSeenArticle: "a1", Status: "pending"}
	high := out.DictionaryCandidate{Term: "Ezo Quarrington", SessionID: "sess-1", CategoryGuess: "nfl_people_context", SupportCount: 5, FirstSeenArticle: "a1", Status: "pending"}
	if err := repo.Save(ctx, low); err != nil {
		t.Fatalf("Save(low) error = %v", err)
	}
	if err := repo.Save(ctx, high); err != nil {
		t.Fatalf("Save(high) error = %v", err)
	}
	// Re-saving an existing term must not clobber it (idempotent mining re-run).
	if err := repo.Save(ctx, out.DictionaryCandidate{Term: "Ezo Quarrington", SessionID: "sess-1", SupportCount: 999, FirstSeenArticle: "a1", Status: "pending"}); err != nil {
		t.Fatalf("Save(duplicate) error = %v", err)
	}

	got, err := repo.ListBySession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}
	if len(got) != 2 || got[0].Term != "Ezo Quarrington" || got[0].SupportCount != 5 {
		t.Fatalf("ListBySession() = %+v, want [Ezo Quarrington(5), training camp(2)] with the original SupportCount preserved", got)
	}
}

func TestDictionaryCandidateRepository_UpdateDecision(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	if err := sessions.Create(ctx, out.EditorialCycleSession{ID: "sess-1", StartedAt: now, UpdatedAt: now, Status: "completed", CurrentStep: "review"}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(ctx, article.Article{ID: "a1", OriginalURL: "https://example.com/1", CanonicalURL: "https://example.com/1", FetchedAt: now, PublishedAt: now, Status: article.StatusPublished}); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	repo := sqliteadapter.NewDictionaryCandidateRepository(db)
	if err := repo.Save(ctx, out.DictionaryCandidate{Term: "term-x", SessionID: "sess-1", SupportCount: 1, FirstSeenArticle: "a1", Status: "pending"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	decidedAt := now.Add(time.Hour)
	if err := repo.UpdateDecision(ctx, "term-x", "approved", "coaching_staff", decidedAt); err != nil {
		t.Fatalf("UpdateDecision() error = %v", err)
	}

	got, err := repo.ListBySession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}
	if len(got) != 1 || got[0].Status != "approved" || got[0].CategoryGuess != "coaching_staff" || !got[0].DecidedAt.Equal(decidedAt) {
		t.Fatalf("ListBySession() = %+v, want status=approved category=coaching_staff decided_at=%v", got, decidedAt)
	}
}
