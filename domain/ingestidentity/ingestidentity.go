// Package ingestidentity resolves a stable, namespaced identity for a
// fetched item — the key ProcessCycle uses to recognize "have I already
// decided this remote item's fate" before running any of the dictionary
// matcher, classifier, dedup cascade, clustering, or publish. It has no
// dependency on storage, HTTP, or config — the caller supplies an
// already-normalized canonical URL (normalization.NormalizeURL) and
// whichever remote ID the adapter trusts (empty when none, or when the
// adapter's only candidate is a known-unstable synthetic value — see the
// design doc's Twitter fix).
package ingestidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// Basis identifies which signal an Identity was derived from.
type Basis string

const (
	BasisRemoteID     Basis = "remote_id"
	BasisCanonicalURL Basis = "canonical_url"
)

// Algorithm is stamped on every resolved Identity. Bump it (and branch on
// the recorded value at the call site) if the derivation rule changes
// enough that historical keys must be treated as a distinct namespace
// rather than silently reinterpreted.
const Algorithm = "v1"

// Identity is the stable, namespaced key one fetched item resolves to.
// Key is opaque (a hash, not the raw ID/URL) so it never leaks which
// signal won, and so a remote-id-basis key can never accidentally collide
// with a canonical-url-basis key even on equal raw input.
type Identity struct {
	Platform  string
	SourceID  string
	Basis     Basis
	Key       string
	Algorithm string
}

// Resolve derives a platform+sourceID-namespaced Identity for one fetched
// item. remoteID (if non-empty) always wins over canonicalURL. Returns the
// zero Identity if both are empty — callers must not invoke Resolve before
// a non-empty canonical URL is guaranteed (article.Validate).
func Resolve(platform, sourceID, remoteID, canonicalURL string) Identity {
	switch {
	case remoteID != "":
		return Identity{
			Platform: platform, SourceID: sourceID, Basis: BasisRemoteID,
			Key:       hashKey(platform, sourceID, BasisRemoteID, remoteID),
			Algorithm: Algorithm,
		}
	case canonicalURL != "":
		return Identity{
			Platform: platform, SourceID: sourceID, Basis: BasisCanonicalURL,
			Key:       hashKey(platform, sourceID, BasisCanonicalURL, canonicalURL),
			Algorithm: Algorithm,
		}
	default:
		return Identity{}
	}
}

func hashKey(platform, sourceID string, basis Basis, input string) string {
	sum := sha256.Sum256([]byte(Algorithm + "|" + platform + "|" + sourceID + "|" + string(basis) + "|" + input))
	return hex.EncodeToString(sum[:])
}

// ContentHash returns a deterministic hash of an item's visible editorial
// content — deliberately excluding PublishedAt/FetchedAt/Metadata, which
// can shift across an identical refetch without representing a real edit.
// Field boundaries are delimited (not concatenated) so "ab"+"c" and
// "a"+"bc" never collide.
func ContentHash(canonicalURL, title, summary string) string {
	const sep = "\x1f"
	sum := sha256.Sum256([]byte(canonicalURL + sep + title + sep + summary))
	return hex.EncodeToString(sum[:])
}

// IsCollision reports whether a claim whose previous terminal outcome's
// canonical URL differs from the incoming one looks like an identity
// collision (GUID recycling, a deleted-and-recreated remote ID) rather
// than a legitimate content edit — only meaningful under BasisRemoteID
// (under BasisCanonicalURL the URL itself is the identity, so a URL change
// there is definitionally a different identity, not a collision to flag),
// and only when a previous URL was actually recorded (a backfilled row
// with no stored URL is not solid evidence either way).
func IsCollision(basis Basis, previousCanonicalURL, incomingCanonicalURL string) bool {
	if basis != BasisRemoteID || previousCanonicalURL == "" || incomingCanonicalURL == "" {
		return false
	}
	prevHost := hostOf(previousCanonicalURL)
	incomingHost := hostOf(incomingCanonicalURL)
	if prevHost == "" || incomingHost == "" {
		return false
	}
	return prevHost != incomingHost
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
