package deduplication

import (
	"time"
)

// ContentFingerprint returns a deterministic identifier for the
// dedup-normalized text of an article (its normalization.Representations.ForDedup),
// covering reposts with a different URL but identical text.
func ContentFingerprint(forDedupText string) string {
	return hashLower(forDedupText)
}

// AttributeCombo returns a deterministic identifier combining a source, its
// dedup-normalized title, and its published time rounded down to window.
// Used only when no external ID is available; deliberately strict
// (identical text, same source, near-simultaneous) so it stays an
// exact-duplicate signal rather than a disguised near-duplicate one.
func AttributeCombo(sourceID, forDedupTitle string, publishedAt time.Time, window time.Duration) string {
	rounded := publishedAt.UTC()
	if window > 0 {
		rounded = rounded.Truncate(window)
	}
	return hashLower(sourceID + "|" + forDedupTitle + "|" + rounded.Format(time.RFC3339))
}

// URLAlias returns a deterministic identifier for a URL considered an
// alternate identity of an already-normalized canonical URL (e.g. the
// pre-normalization original URL an article was fetched from).
func URLAlias(rawURL string) string {
	return hashLower(rawURL)
}
