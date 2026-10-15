package telegram

import (
	"unicode"
	"unicode/utf16"
)

// utf16Len returns the length of s in UTF-16 code units, matching how
// Telegram's Bot API measures text/caption length and entity offsets (see
// https://core.telegram.org/api/entities#entity-length): codepoints in the
// Basic Multilingual Plane count as 1 unit, codepoints outside it (most
// emoji, among others) count as 2 units (a surrogate pair). It counts
// without allocating.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += runeUTF16Len(r)
	}
	return n
}

// ellipsisUTF16Len is utf16Len("…"): U+2026 is in the BMP, so it always
// costs exactly 1 UTF-16 code unit.
const ellipsisUTF16Len = 1

// truncateSummaryHTML returns escapeHTML(raw), truncated if necessary so
// that it fits within budget UTF-16 code units. If it fits already, it is
// returned unchanged (no ellipsis); budget <= 1 returns "".
//
// Truncation operates on raw's codepoints, not on the escaped string: the
// budget for each codepoint is charged at its post-escape UTF-16 cost
// (escapedRuneUTF16Cost), so the cut never lands inside an "&amp;"/"&lt;"/
// "&gt;" escape sequence, and escapeHTML is only applied once, to the
// already-truncated substring.
func truncateSummaryHTML(raw string, budget int) string {
	if budget <= 1 {
		return ""
	}
	// []rune(raw) also normalizes any invalid UTF-8 byte sequence in raw to
	// U+FFFD, guaranteeing the result below is always valid UTF-8 even if
	// the input wasn't.
	runes := []rune(raw)
	full := escapeHTML(string(runes))
	if utf16Len(full) <= budget {
		return full
	}

	limit := budget - ellipsisUTF16Len
	units, cut := 0, 0
	for i, r := range runes {
		c := escapedRuneUTF16Cost(r)
		if units+c > limit {
			break
		}
		units += c
		cut = i + 1
	}
	cut = safeBoundary(runes, cut)
	return escapeHTML(string(runes[:cut])) + "…"
}

// maxClusterBacktrack bounds how far safeBoundary will back off from a
// UTF-16-budget-chosen cut point to avoid splitting a grapheme cluster. A
// small, fixed bound keeps this O(1)-ish rather than a full grapheme
// segmentation pass, and is enough for the joiner/modifier/flag sequences
// this bot's content realistically contains (news article summaries).
const maxClusterBacktrack = 8

// safeBoundary nudges a rune-index cut point (keep runes[:cut], drop the
// rest) backward, if needed, so it doesn't split a grapheme cluster this
// package knows how to recognize: a ZWJ-joined sequence, a variation
// selector, a skin-tone modifier, a combining mark, or one half of a
// regional-indicator flag pair. This is a deliberately lightweight
// heuristic (no grapheme-cluster library dependency), not full UAX#29
// segmentation — it covers the common emoji sequences, not every case.
func safeBoundary(runes []rune, cut int) int {
	for tries := 0; tries < maxClusterBacktrack && cut > 0; tries++ {
		if !breaksCluster(runes, cut) {
			return cut
		}
		cut--
	}
	return cut
}

// breaksCluster reports whether cutting runes right before index cut (i.e.
// keeping runes[:cut]) would split a recognized grapheme sequence.
func breaksCluster(runes []rune, cut int) bool {
	prev := runes[cut-1]
	if isZWJ(prev) {
		return true // a joiner with nothing joined after it
	}
	if cut < len(runes) {
		next := runes[cut]
		if isZWJ(next) || isVariationSelector(next) || isSkinToneModifier(next) || isCombiningMark(next) {
			return true // the excluded rune only modifies/joins the one we're about to keep as the last
		}
	}
	if isRegionalIndicator(prev) {
		n := 0
		for i := cut - 1; i >= 0 && isRegionalIndicator(runes[i]); i-- {
			n++
		}
		if n%2 == 1 {
			return true // flags are regional-indicator pairs; an odd run ends mid-pair
		}
	}
	return false
}

func isZWJ(r rune) bool { return r == '\u200D' }

func isVariationSelector(r rune) bool { return r == '\uFE0F' || r == '\uFE0E' }

func isSkinToneModifier(r rune) bool { return r >= 0x1F3FB && r <= 0x1F3FF }

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isCombiningMark(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r)
}

// escapedRuneUTF16Cost returns the UTF-16 code unit cost r will have once
// escapeHTML runs on it: & becomes "&amp;" (5 units), < becomes "&lt;" (4
// units), > becomes "&gt;" (4 units), everything else is unescaped.
func escapedRuneUTF16Cost(r rune) int {
	switch r {
	case '&':
		return 5
	case '<':
		return 4
	case '>':
		return 4
	default:
		return runeUTF16Len(r)
	}
}

// runeUTF16Len returns the UTF-16 code unit count of r. utf16.RuneLen
// returns -1 for a value that cannot be encoded (a lone surrogate half or
// out-of-range codepoint); range-over-string never yields such a value
// (invalid UTF-8 decodes to utf8.RuneError, which is encodable), but the
// guard keeps this total even if called with a synthetic invalid rune.
func runeUTF16Len(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1
}
