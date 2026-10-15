package sqlite

import (
	"database/sql"
	"errors"
)

// wrapNotFound classifies a raw repository error: if err is (or wraps)
// sql.ErrNoRows, it returns an error that satisfies errors.Is against both
// domainErr and sql.ErrNoRows, so application code can check the
// domain-level sentinel without importing database/sql while every
// existing adapter test asserting errors.Is(err, sql.ErrNoRows) keeps
// working. Any other error (locked, corrupt, I/O, canceled, ...) is
// returned unchanged — it is not a "not found" and must propagate as-is
// for the caller to see its own errors.Is chain (context.Canceled, driver
// error codes, ...).
func wrapNotFound(domainErr, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(domainErr, err)
	}
	return err
}
