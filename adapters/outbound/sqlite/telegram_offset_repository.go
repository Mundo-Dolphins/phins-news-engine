package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// TelegramOffsetRepository implements ports/out.TelegramOffsetRepository.
type TelegramOffsetRepository struct {
	db  *sql.DB
	now func() time.Time
}

// NewTelegramOffsetRepository builds the repository over a migrated database.
func NewTelegramOffsetRepository(db *sql.DB) *TelegramOffsetRepository {
	return &TelegramOffsetRepository{db: db, now: time.Now}
}

var _ appout.TelegramOffsetRepository = (*TelegramOffsetRepository)(nil)

// Load implements ports/out.TelegramOffsetRepository.
func (r *TelegramOffsetRepository) Load(ctx context.Context, bot string) (int64, error) {
	var next int64
	err := executor(ctx, r.db).QueryRowContext(ctx,
		`SELECT next_offset FROM telegram_update_offsets WHERE bot = ?`, bot).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sqlite: load telegram offset: %w", err)
	}
	return next, nil
}

// Store implements ports/out.TelegramOffsetRepository.
func (r *TelegramOffsetRepository) Store(ctx context.Context, bot string, next int64) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO telegram_update_offsets (bot, next_offset, updated_at) VALUES (?,?,?)
		ON CONFLICT(bot) DO UPDATE SET next_offset = excluded.next_offset, updated_at = excluded.updated_at
	`, bot, next, sqliteTime(r.now()))
	if err != nil {
		return fmt.Errorf("sqlite: store telegram offset: %w", err)
	}
	return nil
}
