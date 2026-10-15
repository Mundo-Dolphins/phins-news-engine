package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ModelRegistryRepository implements ports/out.ModelRegistryRepository
// against SQLite.
type ModelRegistryRepository struct {
	db *sql.DB
}

// NewModelRegistryRepository builds a ModelRegistryRepository over an
// already-open, already-migrated database.
func NewModelRegistryRepository(db *sql.DB) *ModelRegistryRepository {
	return &ModelRegistryRepository{db: db}
}

var _ appout.ModelRegistryRepository = (*ModelRegistryRepository)(nil)

// Register implements ports/out.ModelRegistryRepository.
func (r *ModelRegistryRepository) Register(ctx context.Context, m training.ModelMetadata) error {
	status := m.Status
	if status == "" {
		status = "candidate"
	}
	_, err := executor(ctx, r.db).ExecContext(ctx, `
		INSERT INTO model_registry (
			version, kind, path, trained_at, promoted_at, metrics,
			algorithm, feature_schema_version, dataset_hash, thresholds, config, artifact_hash, status
		)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(version) DO UPDATE SET
			kind=excluded.kind, path=excluded.path, trained_at=excluded.trained_at,
			promoted_at=excluded.promoted_at, metrics=excluded.metrics,
			algorithm=excluded.algorithm, feature_schema_version=excluded.feature_schema_version,
			dataset_hash=excluded.dataset_hash, thresholds=excluded.thresholds,
			config=excluded.config, artifact_hash=excluded.artifact_hash, status=excluded.status
	`, m.Version, m.Kind, m.Path, nullableTime(m.TrainedAt), nullableTime(m.PromotedAt), m.Metrics,
		m.Algorithm, m.FeatureSchemaVersion, m.DatasetHash, m.Thresholds, m.Config, m.ArtifactHash, status)
	if err != nil {
		return fmt.Errorf("sqlite: register model %s: %w", m.Version, err)
	}
	return nil
}

// Get implements ports/out.ModelRegistryRepository.
func (r *ModelRegistryRepository) Get(ctx context.Context, version string) (training.ModelMetadata, error) {
	var m training.ModelMetadata
	var trainedAt, promotedAt sqliteNullTime
	err := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT version, kind, path, trained_at, promoted_at, metrics,
			algorithm, feature_schema_version, dataset_hash, thresholds, config, artifact_hash, status
		FROM model_registry WHERE version = ?
	`, version).Scan(&m.Version, &m.Kind, &m.Path, &trainedAt, &promotedAt, &m.Metrics,
		&m.Algorithm, &m.FeatureSchemaVersion, &m.DatasetHash, &m.Thresholds, &m.Config, &m.ArtifactHash, &m.Status)
	if err != nil {
		return training.ModelMetadata{}, fmt.Errorf("sqlite: get model %s: %w", version, err)
	}
	if trainedAt.Valid {
		m.TrainedAt = trainedAt.Time
	}
	if promotedAt.Valid {
		m.PromotedAt = promotedAt.Time
	}
	return m, nil
}

// ListByStatus implements ports/out.ModelRegistryRepository.
func (r *ModelRegistryRepository) ListByStatus(ctx context.Context, status string) ([]training.ModelMetadata, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT version, kind, path, trained_at, promoted_at, metrics,
			algorithm, feature_schema_version, dataset_hash, thresholds, config, artifact_hash, status
		FROM model_registry WHERE status = ? ORDER BY created_at DESC
	`, status)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list models by status %s: %w", status, err)
	}
	defer rows.Close()

	var out []training.ModelMetadata
	for rows.Next() {
		var m training.ModelMetadata
		var trainedAt, promotedAt sqliteNullTime
		if err := rows.Scan(&m.Version, &m.Kind, &m.Path, &trainedAt, &promotedAt, &m.Metrics,
			&m.Algorithm, &m.FeatureSchemaVersion, &m.DatasetHash, &m.Thresholds, &m.Config, &m.ArtifactHash, &m.Status); err != nil {
			return nil, fmt.Errorf("sqlite: scan model registry row: %w", err)
		}
		if trainedAt.Valid {
			m.TrainedAt = trainedAt.Time
		}
		if promotedAt.Valid {
			m.PromotedAt = promotedAt.Time
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate model registry rows: %w", err)
	}
	return out, nil
}
