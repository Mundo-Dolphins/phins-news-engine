package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// TrainingRunRepository implements ports/out.TrainingRunRepository
// against SQLite.
type TrainingRunRepository struct {
	db *sql.DB
}

// NewTrainingRunRepository builds a TrainingRunRepository over an
// already-open, already-migrated database.
func NewTrainingRunRepository(db *sql.DB) *TrainingRunRepository {
	return &TrainingRunRepository{db: db}
}

var _ appout.TrainingRunRepository = (*TrainingRunRepository)(nil)

// nullIfEmpty maps an empty model version to SQL NULL.
// training_runs.model_version is a FOREIGN KEY to
// model_registry.version (migration 0005) and the connection runs with
// PRAGMA foreign_keys=ON, so a run that promoted nothing -- a gate
// rejection, or any stage failure -- must store NULL rather than a
// non-existent version string: a NULL foreign key value is permitted,
// only a non-NULL one must reference an existing row.
func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// Save implements ports/out.TrainingRunRepository.
func (r *TrainingRunRepository) Save(ctx context.Context, run training.Run) error {
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO training_runs (id, started_at, finished_at, model_version, dataset_summary, status, report)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			started_at=excluded.started_at, finished_at=excluded.finished_at,
			model_version=excluded.model_version, dataset_summary=excluded.dataset_summary,
			status=excluded.status, report=excluded.report
	`, run.ID, sqliteTime(run.StartedAt), sqliteTime(run.FinishedAt), nullIfEmpty(run.ModelVersion), run.DatasetSummary, run.Status, run.Report)
	if err != nil {
		return fmt.Errorf("sqlite: save training run %s: %w", run.ID, err)
	}
	return nil
}

// Latest implements ports/out.TrainingRunRepository.
func (r *TrainingRunRepository) Latest(ctx context.Context) (*training.Run, error) {
	row := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT id, started_at, finished_at, model_version, dataset_summary, status, report
		FROM training_runs ORDER BY started_at DESC LIMIT 1
	`)
	var run training.Run
	var startedAt, finishedAt sqliteNullTime
	var modelVersion sql.NullString // NULL for runs that promoted nothing — see nullIfEmpty
	if err := row.Scan(&run.ID, &startedAt, &finishedAt, &modelVersion, &run.DatasetSummary, &run.Status, &run.Report); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlite: get latest training run: %w", err)
	}
	run.ModelVersion = modelVersion.String
	if startedAt.Valid {
		run.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		run.FinishedAt = finishedAt.Time
	}
	return &run, nil
}

// LatestSucceeded implements ports/out.TrainingRunRepository.
func (r *TrainingRunRepository) LatestSucceeded(ctx context.Context) (*training.Run, error) {
	row := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT id, started_at, finished_at, model_version, dataset_summary, status, report
		FROM training_runs WHERE status = 'succeeded' ORDER BY started_at DESC LIMIT 1
	`)
	var run training.Run
	var startedAt, finishedAt sqliteNullTime
	var modelVersion sql.NullString // NULL for runs that promoted nothing — see nullIfEmpty
	if err := row.Scan(&run.ID, &startedAt, &finishedAt, &modelVersion, &run.DatasetSummary, &run.Status, &run.Report); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlite: get latest succeeded training run: %w", err)
	}
	run.ModelVersion = modelVersion.String
	if startedAt.Valid {
		run.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		run.FinishedAt = finishedAt.Time
	}
	return &run, nil
}
