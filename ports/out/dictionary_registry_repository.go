package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

// DictionaryRegistryRepository persists dictionary version metadata.
// Promoted versions (Registration.Promoted() == true) are never deleted by
// retention.
type DictionaryRegistryRepository interface {
	// Register inserts r, or updates it in place if its Version already
	// exists.
	Register(ctx context.Context, r dictionary.Registration) error
	// Get returns the dictionary registration with the given version, or a
	// wrapped sql.ErrNoRows if none exists.
	Get(ctx context.Context, version string) (dictionary.Registration, error)
}
