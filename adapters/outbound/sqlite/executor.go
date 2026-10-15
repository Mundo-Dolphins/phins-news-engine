package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// dbtx is satisfied by both *sql.DB and *sql.Tx: every repository method in
// this package executes through this instead of a concrete type, so it
// works transparently whether or not a UnitOfWork transaction is in flight
// on ctx.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type txKey struct{}

// executor returns the in-flight transaction stashed in ctx by
// UnitOfWork.Do, or fallback if there is none.
func executor(ctx context.Context, fallback dbtx) dbtx {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return fallback
}

// withTx runs fn inside its own short-lived transaction on db, guaranteeing
// its multiple statements commit or roll back together even when called on
// its own. Use it for a repository method that must atomically touch more
// than one table but isn't already guaranteed to run inside a
// caller-provided UnitOfWork transaction.
//
// withTx is reentrant, exactly like UnitOfWork.Do: if ctx already carries a
// transaction (stashed by an outer UnitOfWork.Do or withTx call), it joins
// that transaction — running fn against it directly — instead of starting
// a second, independent one. Under this project's single-connection pool
// (SetMaxOpenConns(1)), a nested BeginTx would need a second connection the
// pool cannot hand out while the outer transaction holds the only one,
// deadlocking rather than erroring. Joining also means fn's statements
// become part of whatever atomic unit the outer caller owns — the outer
// Do/withTx call is the one that commits or rolls back.
func withTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return fn(tx)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit transaction: %w", err)
	}
	return nil
}
