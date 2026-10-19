// Package sourcefilter implements the deterministic exclusion-list filters
// (blocked keywords/URLs, required keywords, skipped authors) shared by
// rss, bluesky, and twitter — ported once from v1, where each fetcher
// duplicated this logic. It has nothing to do with topical relevance
// (Phase 4's classification.Decide already covers that).
// It also holds ContentTypeFor, a mapping every fetcher package needs
// regardless of whether it uses the exclusion filters.
package sourcefilter

import (
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// ContainsBlockedKeyword reports whether any of values contains (case-
// insensitive substring match) any of blockedKeywords.
func ContainsBlockedKeyword(values []string, blockedKeywords []string) bool {
	for _, blocked := range blockedKeywords {
		normalized := strings.ToLower(strings.TrimSpace(blocked))
		if normalized == "" {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), normalized) {
				return true
			}
		}
	}
	return false
}

// ContainsBlockedKeywordUnless is ContainsBlockedKeyword with an escape hatch:
// an item that contains any of keepKeywords is never blocked by keyword. A
// blocked keyword is a coarse guard against off-topic chatter ("Hurricanes"),
// but the same word shows up in on-topic items (NFL scouts at a college game
// between two teams); keepKeywords names those. Blocked URLs and skipped
// authors are separate filters and are not affected.
func ContainsBlockedKeywordUnless(values []string, blockedKeywords, keepKeywords []string) bool {
	if !ContainsBlockedKeyword(values, blockedKeywords) {
		return false
	}
	return !containsAnyKeyword(values, keepKeywords)
}

func containsAnyKeyword(values []string, keywords []string) bool {
	for _, keep := range keywords {
		normalized := strings.ToLower(strings.TrimSpace(keep))
		if normalized == "" {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), normalized) {
				return true
			}
		}
	}
	return false
}

// ContainsRequiredKeyword reports whether any of values contains any of
// requiredKeywords. An empty requiredKeywords list means "no requirement"
// — always true.
func ContainsRequiredKeyword(values []string, requiredKeywords []string) bool {
	if len(requiredKeywords) == 0 {
		return true
	}
	for _, required := range requiredKeywords {
		normalized := strings.ToLower(strings.TrimSpace(required))
		if normalized == "" {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), normalized) {
				return true
			}
		}
	}
	return false
}

// IsBridgeErrorItem reports whether a feed item is an RSS-bridge diagnostic
// entry rather than real source content.
func IsBridgeErrorItem(values ...string) bool {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if strings.HasPrefix(normalized, "bridge returned error") {
			return true
		}
	}
	return false
}

// ContainsBlockedURL reports whether any of values contains (case-
// insensitive substring match, scheme- and trailing-slash-insensitive) any
// of blockedURLs.
func ContainsBlockedURL(values []string, blockedURLs []string) bool {
	for _, blocked := range blockedURLs {
		normalized := strings.ToLower(strings.TrimSpace(blocked))
		normalized = strings.TrimPrefix(normalized, "https://")
		normalized = strings.TrimPrefix(normalized, "http://")
		normalized = strings.TrimSuffix(normalized, "/")
		if normalized == "" {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), normalized) {
				return true
			}
		}
	}
	return false
}

// IsSkippedAuthor reports whether author (case-insensitive, leading '@'
// trimmed) is in skipAuthors.
func IsSkippedAuthor(author string, skipAuthors []string) bool {
	if author == "" || len(skipAuthors) == 0 {
		return false
	}
	name := strings.ToLower(strings.TrimLeft(strings.TrimSpace(author), "@"))
	if name == "" {
		return false
	}
	for _, skip := range skipAuthors {
		if name == strings.ToLower(strings.TrimLeft(strings.TrimSpace(skip), "@")) {
			return true
		}
	}
	return false
}

// ContentTypeFor maps a source's configured source_type label (the same
// vocabulary FreshnessConfig's hour buckets use: hard_news/social/analysis)
// to the domain's ContentType, so publishing.FreshnessPolicy.MaxAge picks
// the right freshness window. An unrecognized or empty label is "unknown",
// handled via DefaultHours.
func ContentTypeFor(sourceType string) article.ContentType {
	switch sourceType {
	case "hard_news":
		return article.ContentTypeArticle
	case "social":
		return article.ContentTypeSocialPost
	case "analysis":
		return article.ContentTypeVideo
	default:
		return article.ContentTypeUnknown
	}
}
