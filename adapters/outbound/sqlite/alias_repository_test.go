package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func insertTestArticle(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	articles := sqliteadapter.NewArticleRepository(db)
	err := articles.Save(context.Background(), article.Article{
		ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		FetchedAt: time.Now(), Status: article.StatusFetched,
	})
	if err != nil {
		t.Fatalf("insertTestArticle(%s): %v", id, err)
	}
}

func TestAliasRepository_SaveAndFindArticleID(t *testing.T) {
	db := newTestDB(t)
	insertTestArticle(t, db, "article-1")
	repo := sqliteadapter.NewAliasRepository(db)
	ctx := context.Background()

	if err := repo.Save(ctx, appout.AliasTypeExternalID, "source-1:ext-42", "article-1"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := repo.FindArticleID(ctx, appout.AliasTypeExternalID, "source-1:ext-42")
	if err != nil {
		t.Fatalf("FindArticleID() error = %v", err)
	}
	if got != "article-1" {
		t.Errorf("FindArticleID() = %q, want article-1", got)
	}
}

func TestAliasRepository_FindArticleID_NotFoundWrapsSQLNoRows(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewAliasRepository(db)

	_, err := repo.FindArticleID(context.Background(), appout.AliasTypeURL, "unknown")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindArticleID() error = %v, want wrapped sql.ErrNoRows", err)
	}
}

func TestAliasRepository_Save_DuplicateAliasValueIsRejected(t *testing.T) {
	db := newTestDB(t)
	insertTestArticle(t, db, "article-1")
	insertTestArticle(t, db, "article-2")
	repo := sqliteadapter.NewAliasRepository(db)
	ctx := context.Background()

	if err := repo.Save(ctx, appout.AliasTypeContentFingerprint, "fp-abc", "article-1"); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	err := repo.Save(ctx, appout.AliasTypeContentFingerprint, "fp-abc", "article-2")
	if !errors.Is(err, appout.ErrAliasAlreadyExists) {
		t.Errorf("second Save() error = %v, want wrapped ErrAliasAlreadyExists", err)
	}
}

func TestAliasRepository_DifferentAliasTypesWithSameValueDoNotCollide(t *testing.T) {
	db := newTestDB(t)
	insertTestArticle(t, db, "article-1")
	insertTestArticle(t, db, "article-2")
	repo := sqliteadapter.NewAliasRepository(db)
	ctx := context.Background()

	if err := repo.Save(ctx, appout.AliasTypeURL, "same-value", "article-1"); err != nil {
		t.Fatalf("Save(url) error = %v", err)
	}
	if err := repo.Save(ctx, appout.AliasTypeAttributeCombo, "same-value", "article-2"); err != nil {
		t.Fatalf("Save(attribute_combo) error = %v, want no collision across alias types", err)
	}
}
