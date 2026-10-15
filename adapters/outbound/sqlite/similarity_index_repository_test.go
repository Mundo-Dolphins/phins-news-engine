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

func seedCandidateArticle(t *testing.T, articles *sqliteadapter.ArticleRepository, id, title string, contentType article.ContentType, status article.Status, publishedAt time.Time) {
	t.Helper()
	err := articles.Save(context.Background(), article.Article{
		ID:           id,
		OriginalURL:  "https://example.com/" + id,
		CanonicalURL: "https://example.com/" + id,
		Title:        title,
		Source:       source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Name: "Source One", Type: source.TypeRSS},
		ContentType:  contentType,
		Status:       status,
		PublishedAt:  publishedAt,
		FetchedAt:    publishedAt,
	})
	if err != nil {
		t.Fatalf("seedCandidateArticle(%s): %v", id, err)
	}
}

func TestSimilarityIndexRepository_IndexTermsThenFindCandidatesByMinSharedTerms(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seedCandidateArticle(t, articles, "match", "Isotopes Beat Sharks", article.ContentTypeArticle, article.StatusClustered, now.Add(-1*time.Hour))
	seedCandidateArticle(t, articles, "no-match", "Owls Sign Free Agent", article.ContentTypeArticle, article.StatusClustered, now.Add(-1*time.Hour))

	if err := repo.IndexTerms(ctx, "match", []string{"isotopes beat", "beat sharks"}); err != nil {
		t.Fatalf("IndexTerms(match) error = %v", err)
	}
	if err := repo.IndexTerms(ctx, "no-match", []string{"owls sign", "sign agent"}); err != nil {
		t.Fatalf("IndexTerms(no-match) error = %v", err)
	}

	candidates, err := repo.FindCandidates(ctx, appout.CandidateFilter{
		WindowStart:    now.Add(-72 * time.Hour),
		ContentType:    article.ContentTypeArticle,
		Terms:          []string{"isotopes beat", "beat sharks"},
		MinSharedTerms: 1,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("FindCandidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].ArticleID != "match" {
		t.Fatalf("candidates = %+v, want exactly [match]", candidates)
	}
	if candidates[0].Title != "Isotopes Beat Sharks" || candidates[0].SourceID != "rss:source-1" {
		t.Errorf("candidate = %+v, want full row hydrated", candidates[0])
	}
}

func TestSimilarityIndexRepository_FindCandidatesParsesLegacyPublishedAt(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	seedCandidateArticle(t, articles, "legacy", "Isotopes Sign Veteran", article.ContentTypeArticle, article.StatusClustered, now.Add(-1*time.Hour))
	if _, err := db.ExecContext(ctx, `
		UPDATE articles
		SET published_at = '2026-09-07 13:41:20 -0400 -0400'
		WHERE id = 'legacy'
	`); err != nil {
		t.Fatalf("seed legacy published_at: %v", err)
	}
	if err := repo.IndexTerms(ctx, "legacy", []string{"isotopes sign"}); err != nil {
		t.Fatalf("IndexTerms() error = %v", err)
	}

	candidates, err := repo.FindCandidates(ctx, appout.CandidateFilter{
		WindowStart:    now.Add(-72 * time.Hour),
		ContentType:    article.ContentTypeArticle,
		Terms:          []string{"isotopes sign"},
		MinSharedTerms: 1,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("FindCandidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].ArticleID != "legacy" {
		t.Fatalf("candidates = %+v, want exactly [legacy]", candidates)
	}
	wantPublished := time.Date(2026, 9, 7, 17, 41, 20, 0, time.UTC)
	if !candidates[0].PublishedAt.Equal(wantPublished) {
		t.Errorf("PublishedAt = %v, want %v", candidates[0].PublishedAt, wantPublished)
	}
}

func TestSimilarityIndexRepository_FindCandidates_RespectsMinSharedTerms(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seedCandidateArticle(t, articles, "one-shared", "Only One Shared Term", article.ContentTypeArticle, article.StatusClustered, now.Add(-1*time.Hour))
	if err := repo.IndexTerms(ctx, "one-shared", []string{"isotopes beat", "unrelated term"}); err != nil {
		t.Fatalf("IndexTerms() error = %v", err)
	}

	candidates, err := repo.FindCandidates(ctx, appout.CandidateFilter{
		WindowStart:    now.Add(-72 * time.Hour),
		ContentType:    article.ContentTypeArticle,
		Terms:          []string{"isotopes beat", "beat sharks"},
		MinSharedTerms: 2,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("FindCandidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none (only 1 shared term, need 2)", candidates)
	}
}

func TestSimilarityIndexRepository_FindCandidates_ExcludesOutsideWindow(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seedCandidateArticle(t, articles, "too-old", "Isotopes Beat Sharks", article.ContentTypeArticle, article.StatusClustered, now.Add(-100*time.Hour))
	if err := repo.IndexTerms(ctx, "too-old", []string{"isotopes beat", "beat sharks"}); err != nil {
		t.Fatalf("IndexTerms() error = %v", err)
	}

	candidates, err := repo.FindCandidates(ctx, appout.CandidateFilter{
		WindowStart:    now.Add(-72 * time.Hour),
		ContentType:    article.ContentTypeArticle,
		Terms:          []string{"isotopes beat", "beat sharks"},
		MinSharedTerms: 1,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("FindCandidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none (published_at outside window)", candidates)
	}
}

func TestSimilarityIndexRepository_FindCandidates_ExcludesConfiguredStatuses(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seedCandidateArticle(t, articles, "rejected", "Isotopes Beat Sharks", article.ContentTypeArticle, article.StatusRejectedDuplicate, now.Add(-1*time.Hour))
	if err := repo.IndexTerms(ctx, "rejected", []string{"isotopes beat", "beat sharks"}); err != nil {
		t.Fatalf("IndexTerms() error = %v", err)
	}

	candidates, err := repo.FindCandidates(ctx, appout.CandidateFilter{
		WindowStart:     now.Add(-72 * time.Hour),
		ContentType:     article.ContentTypeArticle,
		ExcludeStatuses: []article.Status{article.StatusRejectedDuplicate},
		Terms:           []string{"isotopes beat", "beat sharks"},
		MinSharedTerms:  1,
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("FindCandidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none (status excluded)", candidates)
	}
}

func TestSimilarityIndexRepository_FindCandidates_FiltersByContentType(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seedCandidateArticle(t, articles, "video", "Isotopes Beat Sharks", article.ContentTypeVideo, article.StatusClustered, now.Add(-1*time.Hour))
	if err := repo.IndexTerms(ctx, "video", []string{"isotopes beat", "beat sharks"}); err != nil {
		t.Fatalf("IndexTerms() error = %v", err)
	}

	candidates, err := repo.FindCandidates(ctx, appout.CandidateFilter{
		WindowStart:    now.Add(-72 * time.Hour),
		ContentType:    article.ContentTypeArticle,
		Terms:          []string{"isotopes beat", "beat sharks"},
		MinSharedTerms: 1,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("FindCandidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none (content type mismatch: video vs article)", candidates)
	}
}

func TestSimilarityIndexRepository_IndexTerms_DuplicateCallIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()

	seedCandidateArticle(t, articles, "a1", "Title", article.ContentTypeArticle, article.StatusClustered, time.Now())
	if err := repo.IndexTerms(ctx, "a1", []string{"term one", "term two"}); err != nil {
		t.Fatalf("first IndexTerms() error = %v", err)
	}
	if err := repo.IndexTerms(ctx, "a1", []string{"term one", "term two"}); err != nil {
		t.Fatalf("second IndexTerms() error = %v, want idempotent (INSERT OR IGNORE)", err)
	}
}
