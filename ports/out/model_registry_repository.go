package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// ModelRegistryRepository persists trained editorial-classifier model
// metadata — audit bookkeeping only; the runtime never queries this to
// decide which model to load (see infrastructure/editorialmodel). Promoted versions (ModelMetadata.Promoted() == true) are never
// deleted by retention.
type ModelRegistryRepository interface {
	// Register inserts m, or updates it in place if its Version already
	// exists.
	Register(ctx context.Context, m training.ModelMetadata) error
	// Get returns the model with the given version, or a wrapped
	// sql.ErrNoRows if none exists.
	Get(ctx context.Context, version string) (training.ModelMetadata, error)
	// ListByStatus returns every model with the given status, most
	// recently created first.
	ListByStatus(ctx context.Context, status string) ([]training.ModelMetadata, error)
}
