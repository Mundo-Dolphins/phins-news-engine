package normalization

import (
	"html"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// TextConfig configures Normalize.
type TextConfig struct {
	StripEmojis            bool
	StripHashtags          bool
	StripMentions          bool
	StripInlineURLs        bool
	RepeatedPrefixPatterns []string
	// BoilerplatePhrases are literal, case-insensitive phrases (e.g. CTA
	// footers like "Subscribe to our channel.") removed before every
	// other view is derived. Unlike RepeatedPrefixPatterns (regex,
	// dedup-only), these are exact strings and affect ForClassification
	// too, since editorial-classifier/dictionary-mining features are
	// built from it.
	BoilerplatePhrases []string
	ShingleSize        int
	MinTokenLength     int
	StopWords          []string
}

// Representations holds every derived view of a piece of text produced by
// Normalize, without destroying the original.
type Representations struct {
	Original string
	// ForClassification decodes HTML entities, applies Unicode NFC, and
	// collapses whitespace, keeping meaning intact for a future relevance
	// classifier.
	ForClassification string
	// ForDedup is an aggressive normalization for identity comparison:
	// URLs/mentions/hashtags/emoji removed per config, repeated prefixes
	// (e.g. "[BREAKING]") stripped, quotes/punctuation normalized, common
	// abbreviations expanded, lowercased.
	ForDedup string
	// Tokens is ForDedup split on non-word boundaries, filtered by
	// StopWords and MinTokenLength.
	Tokens []string
	// Shingles is word n-grams of ShingleSize over Tokens.
	Shingles []string
	// Truncated reports that the text ended in a social truncation artifact
	// (a trailing "…" / "..."), so it is likely a prefix of a longer text.
	Truncated bool
}

var (
	inlineURLPattern = regexp.MustCompile(`https?://\S+`)
	// mentionPattern strips only the "@" marker, not the handle itself —
	// unlike hashtagPattern, which strips the whole tag. A mentioned name
	// (e.g. "@Sharks") is dedup-relevant article content, not metadata;
	// only the marker that makes it look like a mention is noise.
	mentionPattern = regexp.MustCompile(`@`)
	hashtagPattern = regexp.MustCompile(`#\w+`)
	wordPattern    = regexp.MustCompile(`[\p{L}\p{N}]+`)
	// Generic transport/source decorations, stripped for dedup identity only.
	// Not publisher-specific: feed wrappers such as "[Fan Network: The
	// Topes Report] - ", alert prefixes such as "NEW:"/"BREAKING:", and the
	// ellipsis social platforms append when they cut a long headline.
	bracketPrefixPattern = regexp.MustCompile(`^\s*(?:\[[^\]]{1,80}\]\s*[-–—:|]?\s*)+`)
	alertPrefixPattern   = regexp.MustCompile(`(?i)^\s*(?:(?:new|breaking|just in|update|updated|report|watch|video)\s*[:\-–—|]\s*)+`)
	truncationSuffix     = regexp.MustCompile(`\s*(?:…|\.{3,})\s*$`)
	curlyQuotes          = strings.NewReplacer("“", `"`, "”", `"`, "‘", "'", "’", "'")
)

// abbreviations expands a small, fixed set of common news-copy
// abbreviations before punctuation stripping, so "vs." and "vs" both
// become the same token as a fully spelled-out matchup would use.
var abbreviations = strings.NewReplacer(
	" vs. ", " versus ",
	" vs ", " versus ",
	" & ", " and ",
)

// Normalize derives every Representations view of raw. It never mutates or
// discards raw itself.
func Normalize(raw string, cfg TextConfig) Representations {
	classification := collapseSpace(norm.NFC.String(html.UnescapeString(raw)))
	classification = StripPhrases(classification, cfg.BoilerplatePhrases)

	dedup := bracketPrefixPattern.ReplaceAllString(classification, "")
	dedup = alertPrefixPattern.ReplaceAllString(dedup, "")
	dedup = bracketPrefixPattern.ReplaceAllString(dedup, "")
	truncated := truncationSuffix.MatchString(dedup)
	if truncated {
		dedup = truncationSuffix.ReplaceAllString(dedup, "")
	}
	for _, pattern := range cfg.RepeatedPrefixPatterns {
		dedup = regexp.MustCompile(pattern).ReplaceAllString(dedup, "")
	}
	if cfg.StripInlineURLs {
		dedup = inlineURLPattern.ReplaceAllString(dedup, "")
	}
	if cfg.StripMentions {
		dedup = mentionPattern.ReplaceAllString(dedup, "")
	}
	if cfg.StripHashtags {
		dedup = hashtagPattern.ReplaceAllString(dedup, "")
	}
	if cfg.StripEmojis {
		dedup = stripEmojis(dedup)
	}
	dedup = curlyQuotes.Replace(dedup)
	dedup = " " + dedup + " "
	dedup = abbreviations.Replace(dedup)
	dedup = strings.ToLower(collapseSpace(dedup))

	tokens := filterTokens(wordPattern.FindAllString(dedup, -1), cfg)
	shingles := buildShingles(tokens, cfg.ShingleSize)

	return Representations{
		Original:          raw,
		ForClassification: classification,
		ForDedup:          dedup,
		Tokens:            tokens,
		Shingles:          shingles,
		Truncated:         truncated,
	}
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func stripEmojis(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) {
			return -1
		}
		return r
	}, s)
}

func filterTokens(raw []string, cfg TextConfig) []string {
	stop := make(map[string]bool, len(cfg.StopWords))
	for _, w := range cfg.StopWords {
		stop[strings.ToLower(w)] = true
	}
	tokens := make([]string, 0, len(raw))
	for _, tok := range raw {
		if len(tok) < cfg.MinTokenLength || stop[tok] {
			continue
		}
		tokens = append(tokens, tok)
	}
	return tokens
}

func buildShingles(tokens []string, size int) []string {
	if size <= 0 || len(tokens) < size {
		return nil
	}
	shingles := make([]string, 0, len(tokens)-size+1)
	for i := 0; i+size <= len(tokens); i++ {
		shingles = append(shingles, strings.Join(tokens[i:i+size], " "))
	}
	return shingles
}
