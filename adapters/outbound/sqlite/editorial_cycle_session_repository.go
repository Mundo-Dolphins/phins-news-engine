package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// EditorialCycleSessionRepository implements
// ports/out.EditorialCycleSessionRepository against SQLite.
type EditorialCycleSessionRepository struct {
	db *sql.DB
}

// NewEditorialCycleSessionRepository builds an
// EditorialCycleSessionRepository over an already-open, already-migrated
// database.
func NewEditorialCycleSessionRepository(db *sql.DB) *EditorialCycleSessionRepository {
	return &EditorialCycleSessionRepository{db: db}
}

var _ appout.EditorialCycleSessionRepository = (*EditorialCycleSessionRepository)(nil)

// Create implements ports/out.EditorialCycleSessionRepository.
func (r *EditorialCycleSessionRepository) Create(ctx context.Context, s appout.EditorialCycleSession) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json, total_candidates)
		VALUES (?,?,?,?,?,?,?)
		`, s.ID, sqliteTime(s.StartedAt), sqliteTime(s.UpdatedAt), s.Status, s.CurrentStep, s.FlagsJSON, s.TotalCandidates)
	if err != nil {
		return fmt.Errorf("sqlite: create editorial cycle session %s: %w", s.ID, err)
	}
	return nil
}

// Update implements ports/out.EditorialCycleSessionRepository.
func (r *EditorialCycleSessionRepository) Update(ctx context.Context, s appout.EditorialCycleSession) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE editorial_cycle_sessions SET updated_at = ?, status = ?, current_step = ?, flags_json = ?, total_candidates = ?
		WHERE id = ?
		`, sqliteTime(s.UpdatedAt), s.Status, s.CurrentStep, s.FlagsJSON, s.TotalCandidates, s.ID)
	if err != nil {
		return fmt.Errorf("sqlite: update editorial cycle session %s: %w", s.ID, err)
	}
	return nil
}

// GetLatestRunning returns the most recently updated session whose Status
// is "running", or a wrapped sql.ErrNoRows if none exists.
func (r *EditorialCycleSessionRepository) GetLatestRunning(ctx context.Context) (appout.EditorialCycleSession, error) {
	row := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT id, started_at, updated_at, status, current_step, flags_json, total_candidates
		FROM editorial_cycle_sessions WHERE status = 'running' ORDER BY updated_at DESC LIMIT 1
	`)
	var s appout.EditorialCycleSession
	if err := scanEditorialCycleSession(row, &s); err != nil {
		return appout.EditorialCycleSession{}, fmt.Errorf("sqlite: get latest running editorial cycle session: %w", err)
	}
	return s, nil
}

// GetLatestCompleted implements ports/out.EditorialCycleSessionRepository.
func (r *EditorialCycleSessionRepository) GetLatestCompleted(ctx context.Context) (appout.EditorialCycleSession, error) {
	row := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT id, started_at, updated_at, status, current_step, flags_json, total_candidates
		FROM editorial_cycle_sessions WHERE status = 'completed' ORDER BY updated_at DESC LIMIT 1
	`)
	var s appout.EditorialCycleSession
	if err := scanEditorialCycleSession(row, &s); err != nil {
		return appout.EditorialCycleSession{}, fmt.Errorf("sqlite: get latest completed editorial cycle session: %w", err)
	}
	return s, nil
}

// GetLatestResumable implements ports/out.EditorialCycleSessionRepository.
func (r *EditorialCycleSessionRepository) GetLatestResumable(ctx context.Context) (appout.EditorialCycleSession, error) {
	row := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT id, started_at, updated_at, status, current_step, flags_json, total_candidates
		FROM editorial_cycle_sessions WHERE status IN ('running','paused','failed') ORDER BY updated_at DESC LIMIT 1
	`)
	var s appout.EditorialCycleSession
	if err := scanEditorialCycleSession(row, &s); err != nil {
		return appout.EditorialCycleSession{}, fmt.Errorf("sqlite: get latest resumable editorial cycle session: %w", err)
	}
	return s, nil
}

func scanEditorialCycleSession(row rowScanner, s *appout.EditorialCycleSession) error {
	var startedAt, updatedAt sqliteNullTime
	if err := row.Scan(&s.ID, &startedAt, &updatedAt, &s.Status, &s.CurrentStep, &s.FlagsJSON, &s.TotalCandidates); err != nil {
		return err
	}
	if startedAt.Valid {
		s.StartedAt = startedAt.Time
	}
	if updatedAt.Valid {
		s.UpdatedAt = updatedAt.Time
	}
	return nil
}
