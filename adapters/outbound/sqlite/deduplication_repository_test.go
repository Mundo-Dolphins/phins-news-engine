package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestDeduplicationRepository_SaveAndExists(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewDeduplicationRepository(db)
	ctx := context.Background()

	a := article.Article{ID: "a1", OriginalURL: "https://example.com/story", CanonicalURL: "https://example.com/story", FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusNormalized}
	if err := articles.Save(ctx, a); err != nil {
		t.Fatalf("Save(article) error = %v", err)
	}

	exists, err := repo.Exists(ctx, "fp-1")
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if exists {
		t.Error("Exists() = true before Save, want false")
	}

	if err := repo.Save(ctx, "fp-1", "a1"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	exists, err = repo.Exists(ctx, "fp-1")
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !exists {
		t.Error("Exists() = false after Save, want true")
	}
}

func TestDeduplicationRepository_SaveTwiceReturnsAlreadyExists(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewDeduplicationRepository(db)
	ctx := context.Background()

	a := article.Article{ID: "a1", OriginalURL: "https://example.com/story", CanonicalURL: "https://example.com/story", FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusNormalized}
	if err := articles.Save(ctx, a); err != nil {
		t.Fatalf("Save(article) error = %v", err)
	}
	if err := repo.Save(ctx, "fp-1", "a1"); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	err := repo.Save(ctx, "fp-1", "a1")
	if !errors.Is(err, appout.ErrAlreadyExists) {
		t.Fatalf("second Save() error = %v, want wrapped ErrAlreadyExists", err)
	}
}
