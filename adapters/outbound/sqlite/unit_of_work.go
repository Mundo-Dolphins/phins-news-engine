package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// UnitOfWork implements ports/out.UnitOfWork against SQLite.
type UnitOfWork struct {
	db *sql.DB
}

// NewUnitOfWork builds a UnitOfWork over an already-open, already-migrated
// database.
func NewUnitOfWork(db *sql.DB) *UnitOfWork {
	return &UnitOfWork{db: db}
}

var _ appout.UnitOfWork = (*UnitOfWork)(nil)

// Do implements ports/out.UnitOfWork.
//
// Do is reentrant: if ctx already carries a transaction started by an
// outer Do call, this call joins it — running fn directly against that
// same transaction — instead of starting a second, independent one. This
// matters because the database is opened with SetMaxOpenConns(1) (see
// infrastructure/sqlite.Open): a nested BeginTx would need a second
// connection that the pool cannot hand out while the outer transaction
// still holds the only one, deadlocking. Composing use cases (e.g.
// ProcessCycle wrapping its whole per-article write sequence in one Do,
// while a shared helper like accept() also calls Do internally so it stays
// correct when invoked on its own) rely on this to avoid every caller
// having to know whether it might already be inside a transaction.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, inTx := ctx.Value(txKey{}).(*sql.Tx); inTx {
		return fn(ctx)
	}

	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin unit of work: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := context.WithValue(ctx, txKey{}, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit unit of work: %w", err)
	}
	return nil
}
