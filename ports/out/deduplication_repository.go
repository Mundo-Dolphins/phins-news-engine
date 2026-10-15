package out

import (
	"context"
	"errors"
)

// ErrAlreadyExists is returned by DeduplicationRepository.Save when the
// fingerprint is already recorded against a different (or the same)
// article.
var ErrAlreadyExists = errors.New("sqlite: fingerprint already recorded")

// DeduplicationRepository tracks exact-duplicate fingerprints for the
// canonical (accepted) article that first used them. It is decoupled from
// the articles table's own retention: a fingerprint is only removed when
// the article that owns it is deleted, via ON DELETE CASCADE.
type DeduplicationRepository interface {
	// Exists reports whether fingerprint has already been recorded.
	Exists(ctx context.Context, fingerprint string) (bool, error)
	// Save records fingerprint as belonging to articleID. Calling it twice
	// for the same fingerprint returns an error wrapping ErrAlreadyExists.
	Save(ctx context.Context, fingerprint, articleID string) error
}
