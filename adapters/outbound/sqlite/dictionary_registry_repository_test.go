package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func TestDictionaryRegistryRepository_SeedPlaceholderIsGettable(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryRegistryRepository(db)

	got, err := repo.Get(context.Background(), "placeholder-v0")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Promoted() {
		t.Error("seeded placeholder-v0 should not be promoted")
	}
}

func TestDictionaryRegistryRepository_RegisterAndPromote(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryRegistryRepository(db)
	ctx := context.Background()

	d := dictionary.Registration{Version: "v1", Path: "/dict/v1.yml", EntryCount: 42}
	if err := repo.Register(ctx, d); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got, err := repo.Get(ctx, "v1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.EntryCount != 42 {
		t.Errorf("EntryCount = %d, want 42", got.EntryCount)
	}
	if got.Promoted() {
		t.Error("Promoted() = true before promotion, want false")
	}

	d.PromotedAt = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := repo.Register(ctx, d); err != nil {
		t.Fatalf("re-Register() with promotion error = %v", err)
	}
	got, err = repo.Get(ctx, "v1")
	if err != nil {
		t.Fatalf("Get() after promotion error = %v", err)
	}
	if !got.Promoted() {
		t.Error("Promoted() = false after setting PromotedAt, want true")
	}
}

func TestDictionaryRegistryRepository_Get_NotFound(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryRegistryRepository(db)

	_, err := repo.Get(context.Background(), "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get() error = %v, want wrapped sql.ErrNoRows", err)
	}
}
