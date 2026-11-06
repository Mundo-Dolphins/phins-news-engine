package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

// openPair returns a plain handle (the "bot") and an immediate-tx handle
// (the "admin") on one database file holding table t(v).
func openPair(t *testing.T) (bot, admin *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pair.db")
	bot, err := sqlite.Open(path, 2*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = bot.Close() })
	if _, err := bot.Exec(`CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	admin, err = sqlite.OpenImmediate(path, 5*time.Second)
	if err != nil {
		t.Fatalf("OpenImmediate: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return bot, admin
}

func TestOpenImmediateWaitsForWriter(t *testing.T) {
	bot, admin := openPair(t)
	const hold = 200 * time.Millisecond

	tx, err := bot.Begin()
	if err != nil {
		t.Fatalf("bot begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO t VALUES (1)`); err != nil {
		t.Fatalf("bot write: %v", err)
	}

	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		atx, err := admin.BeginTx(context.Background(), nil)
		if err == nil {
			_, err = atx.Exec(`INSERT INTO t VALUES (2)`)
			if err == nil {
				err = atx.Commit()
			} else {
				_ = atx.Rollback()
			}
		}
		done <- result{err, time.Since(started)}
	}()

	time.Sleep(hold)
	if err := tx.Commit(); err != nil {
		t.Fatalf("bot commit: %v", err)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("admin write failed instead of waiting: %v", r.err)
	}
	if r.elapsed < hold {
		t.Errorf("admin write finished in %v, want >= %v (it should have waited)", r.elapsed, hold)
	}
}

func TestOpenImmediateUpgradeDoesNotFailFast(t *testing.T) {
	bot, admin := openPair(t)
	const hold = 200 * time.Millisecond

	tx, err := bot.Begin()
	if err != nil {
		t.Fatalf("bot begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO t VALUES (1)`); err != nil {
		t.Fatalf("bot write: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		atx, err := admin.BeginTx(context.Background(), nil)
		if err != nil {
			done <- err
			return
		}
		var n int
		if err := atx.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
			_ = atx.Rollback()
			done <- err
			return
		}
		if _, err := atx.Exec(`INSERT INTO t VALUES (2)`); err != nil {
			_ = atx.Rollback()
			done <- err
			return
		}
		done <- atx.Commit()
	}()

	time.Sleep(hold)
	if err := tx.Commit(); err != nil {
		t.Fatalf("bot commit: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("read-then-write tx failed (SQLITE_BUSY?): %v", err)
	}
	var n int
	if err := bot.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows = %d, err = %v, want 2", n, err)
	}
}
