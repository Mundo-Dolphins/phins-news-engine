package sqlite_test

import (
	"context"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func TestIntegrity_CleanDatabasePasses(t *testing.T) {
	db := openMigratedTestDB(t)

	if err := sqlite.Integrity(context.Background(), db); err != nil {
		t.Errorf("Integrity() error = %v, want nil for a freshly migrated database", err)
	}
}
