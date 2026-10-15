package out

import (
	"context"
	"errors"
)

// AliasType identifies which exact-duplicate signal an alias row records.
type AliasType string

const (
	AliasTypeExternalID         AliasType = "external_id"
	AliasTypeURL                AliasType = "url"
	AliasTypeContentFingerprint AliasType = "content_fingerprint"
	AliasTypeAttributeCombo     AliasType = "attribute_combo"
)

// ErrAliasAlreadyExists is returned by AliasRepository.Save when the
// (aliasType, aliasValue) pair is already recorded.
var ErrAliasAlreadyExists = errors.New("sqlite: alias already recorded")

// AliasRepository extends exact-duplicate detection beyond canonical-URL
// fingerprinting: external IDs, alternate URLs, content fingerprints, and
// safe attribute combinations all resolve to the article that first used
// them. See ADR 0009 for the full cascade and why each signal is
// considered "exact" rather than "near".
type AliasRepository interface {
	// FindArticleID returns the article ID already recorded against
	// (aliasType, aliasValue), or a wrapped sql.ErrNoRows if none exists.
	FindArticleID(ctx context.Context, aliasType AliasType, aliasValue string) (string, error)
	// Save records (aliasType, aliasValue) as belonging to articleID.
	// Calling it twice for the same pair returns an error wrapping
	// ErrAliasAlreadyExists.
	Save(ctx context.Context, aliasType AliasType, aliasValue, articleID string) error
}
