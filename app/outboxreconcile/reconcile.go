// Package outboxreconcile matches Telegram channel history back to a
// specific outbox item's expected canonical URL — the deterministic
// reconciliation step ADR 0030 requires before any ambiguous delivery is
// either finalized or retried. Pure and unit-tested; the actual channel
// fetch lives in usecase.PublishOutbox.Reconcile (mirrors
// application/telegramimport's own pure/impure split — see ADR 0021).
package outboxreconcile

import (
	"strconv"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	domaintelegramimport "github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
)

// MatchInHistory searches messages (order-independent — the caller
// typically passes them newest-first, as
// out.TelegramChannelImporter.ExportChannelMessages returns them, but
// MatchInHistory itself scans the whole slice) for one whose extracted,
// normalized URL equals canonicalURL and whose PostedAt is no earlier
// than sentAt.Add(-clockSkew) — the skew guards against the outbox's
// local clock and Telegram's server clock disagreeing by a couple of
// minutes, which would otherwise make a genuine match look like it
// predates the send.
//
// covered reports whether the search reached far enough back in time to
// trust a non-match: true once the oldest message actually inspected has
// PostedAt at or before sentAt.Add(-clockSkew). A caller must never treat
// (_, false, false) as "confirmed not sent" — it means the fetched window
// was too shallow to conclude anything, not that nothing was found.
//
// Return values:
//   - (messageID, true, true) on a confident match — reconcile positive.
//   - ("", false, true) when the search covered back to sentAt.Add(-clockSkew)
//     and found nothing — reconcile negative (confirmed not delivered).
//   - ("", false, false) when messages didn't reach back that far (the
//     channel produced more messages than were fetched, or the fetch was
//     otherwise short, or messages is empty) — inconclusive, caller must
//     not treat this as a negative result.
func MatchInHistory(
	messages []domaintelegramimport.Message, canonicalURL string, sentAt time.Time,
	urlCfg normalization.URLConfig, resolvers []normalization.KnownLinkResolver, clockSkew time.Duration,
) (messageID string, matched bool, covered bool) {
	cutoff := sentAt.Add(-clockSkew)
	var oldestSeen time.Time

	for _, m := range messages {
		if oldestSeen.IsZero() || m.PostedAt.Before(oldestSeen) {
			oldestSeen = m.PostedAt
		}

		raw, ok := telegramimport.ExtractURL(m.Text)
		if !ok {
			continue
		}
		canonical, err := normalization.NormalizeURL(raw, urlCfg, resolvers)
		if err != nil {
			continue
		}
		if canonical == canonicalURL && !m.PostedAt.Before(cutoff) {
			return strconv.Itoa(m.ID), true, true
		}
	}

	if oldestSeen.IsZero() {
		return "", false, false
	}
	return "", false, !oldestSeen.After(cutoff)
}

// Post is one entry of a destination's own post history, reduced to what
// reconciliation compares.
type Post struct {
	ID       string
	Text     string
	PostedAt time.Time
}

// MatchPost is MatchInHistory for destinations that expose their own posts
// with the exact text (Bluesky). A post matches when its text equals text
// and its PostedAt is no earlier than sentAt.Add(-clockSkew). The covered
// result carries the same meaning and the same rules as MatchInHistory: only
// (_, false, true) is a confirmed non-delivery.
func MatchPost(posts []Post, text string, sentAt time.Time, clockSkew time.Duration) (id string, matched, covered bool) {
	cutoff := sentAt.Add(-clockSkew)
	var oldestSeen time.Time
	for _, p := range posts {
		if oldestSeen.IsZero() || p.PostedAt.Before(oldestSeen) {
			oldestSeen = p.PostedAt
		}
		if p.Text == text && !p.PostedAt.Before(cutoff) {
			return p.ID, true, true
		}
	}
	if oldestSeen.IsZero() {
		return "", false, false
	}
	return "", false, !oldestSeen.After(cutoff)
}
