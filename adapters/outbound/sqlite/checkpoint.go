package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Checkpointer implements ports/out.Checkpointer against SQLite.
type Checkpointer struct {
	db *sql.DB
}

// NewCheckpointer builds a Checkpointer over an already-open database.
func NewCheckpointer(db *sql.DB) *Checkpointer {
	return &Checkpointer{db: db}
}

var _ appout.Checkpointer = (*Checkpointer)(nil)

// Checkpoint implements ports/out.Checkpointer.
func (c *Checkpointer) Checkpoint(ctx context.Context) error {
	if _, err := c.db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		return fmt.Errorf("sqlite: wal checkpoint: %w", err)
	}
	return nil
}
