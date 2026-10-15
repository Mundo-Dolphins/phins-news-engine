package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func openTestDBForUnitOfWork(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

func TestUnitOfWork_CommitsOnSuccess(t *testing.T) {
	db := openTestDBForUnitOfWork(t)
	uow := NewUnitOfWork(db)
	ctx := context.Background()

	err := uow.Do(ctx, func(txCtx context.Context) error {
		_, err := executor(txCtx, db).ExecContext(txCtx, `INSERT INTO sources (id, name, type) VALUES ('s1','Source One','rss')`)
		return err
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sources WHERE id = 's1'`).Scan(&count); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (insert should be committed)", count)
	}
}

func TestUnitOfWork_RollsBackOnError(t *testing.T) {
	db := openTestDBForUnitOfWork(t)
	uow := NewUnitOfWork(db)
	ctx := context.Background()
	wantErr := errors.New("downstream failure")

	err := uow.Do(ctx, func(txCtx context.Context) error {
		if _, err := executor(txCtx, db).ExecContext(txCtx, `INSERT INTO sources (id, name, type) VALUES ('s1','Source One','rss')`); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Do() error = %v, want %v", err, wantErr)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sources WHERE id = 's1'`).Scan(&count); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 (insert should be rolled back)", count)
	}
}

func TestExecutor_FallsBackToDBOutsideTransaction(t *testing.T) {
	db := openTestDBForUnitOfWork(t)
	if got := executor(context.Background(), db); got != dbtx(db) {
		t.Errorf("executor() outside a UnitOfWork = %v, want the fallback *sql.DB", got)
	}
}
