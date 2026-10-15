package sqlite_test

import (
	"context"
	"testing"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
)

func TestCheckpointer_Checkpoint(t *testing.T) {
	db := newTestDB(t)
	c := sqliteadapter.NewCheckpointer(db)

	if err := c.Checkpoint(context.Background()); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
}
