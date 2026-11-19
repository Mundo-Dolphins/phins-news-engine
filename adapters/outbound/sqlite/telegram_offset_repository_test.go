package sqlite_test

import (
	"context"
	"testing"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
)

func TestOffsetRepositoryRoundTrip(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewTelegramOffsetRepository(db)
	ctx := context.Background()

	if got, err := repo.Load(ctx, "123"); err != nil || got != 0 {
		t.Fatalf("Load() = %d, %v; want 0, nil", got, err)
	}
	if err := repo.Store(ctx, "123", 42); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if err := repo.Store(ctx, "123", 50); err != nil {
		t.Fatalf("Store() overwrite error = %v", err)
	}
	if got, err := repo.Load(ctx, "123"); err != nil || got != 50 {
		t.Fatalf("Load() = %d, %v; want 50, nil", got, err)
	}
	if got, _ := repo.Load(ctx, "999"); got != 0 {
		t.Fatalf("other bot Load() = %d, want 0", got)
	}
}
