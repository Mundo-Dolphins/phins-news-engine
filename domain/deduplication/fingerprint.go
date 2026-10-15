// Package deduplication provides exact-duplicate detection (canonical-URL
// fingerprinting and alias-based signals) and near-duplicate/same-event
// scoring for articles.
package deduplication

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// Fingerprint returns a deterministic identifier for the canonical URL of a.
// Two articles with the same fingerprint are considered exact duplicates.
func Fingerprint(a article.Article) string {
	return hashLower(a.CanonicalURL)
}

// hashLower returns the hex-encoded SHA-256 of s after trimming and
// lowercasing, the shared primitive behind every exact-identity signal in
// this package.
func hashLower(s string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s))))
	return hex.EncodeToString(sum[:])
}
