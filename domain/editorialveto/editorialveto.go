// Package editorialveto evaluates a fixed set of deterministic,
// configurable rules (spam/gambling/sponsored/noise phrase or regex
// patterns) against article text — a moderation layer independent of
// the trained editorial.Model, so its decisions are always explainable
// by a single reason code rather than a model weight. Pure and
// config-free of its own: the Config it evaluates against is loaded by
// infrastructure/editorialveto, never by this package. See
// domain/editorial for the sibling pattern this mirrors (feature
// extraction, not policy loading).
package editorialveto

import (
	"regexp"
	"strings"
)

// Category groups related veto rules for reporting/impact-measurement
// purposes (editorialtrain.Report.VetoImpact breaks down by Triggered,
// not by Category — Category is carried on Result for callers that do
// want to slice by it).
type Category string

const (
	CategorySpam      Category = "spam"
	CategoryGambling  Category = "gambling"
	CategorySponsored Category = "sponsored"
	CategoryNoise     Category = "noise"
)

// Rule is one named, explainable veto check: any Patterns match triggers
// ReasonCode. Patterns are regexes — either plain phrases like "free bets"
// or explicit regex patterns like `[A-Z]{12,}`. Plain-phrase patterns (those
// lacking regex metacharacters) are automatically word-boundary-wrapped
// (`\b...\b`) and matched case-insensitively; regex patterns (those
// containing metacharacters) are compiled and matched as literally given
// (case-sensitive, no wrapping). This allows config authors writing plain
// phrases like "click here now" to work correctly without manual `\b` escaping,
// while allowing explicit patterns like `[A-Z]{12,}` to retain their intended
// case-sensitivity.
type Rule struct {
	Category   Category
	ReasonCode string
	Patterns   []string
	// ExcludePatterns makes a rule conditional: if any exclusion matches,
	// the rule does not trigger. This is used for high-precision noise rules
	// such as animal references, which must not reject NFL coverage.
	ExcludePatterns []string
}

// Config is the full, ordered set of veto rules — ordered because
// Evaluate reports the first matching rule when more than one matches
// (deterministic, config-author-controlled precedence).
type Config struct {
	Version string
	Rules   []Rule
}

// Result is Evaluate's verdict for one piece of text.
type Result struct {
	Triggered    bool
	ReasonCode   string
	Category     Category
	MatchedTerms []string
}

// isPlainPhrase detects whether a pattern looks like a plain phrase
// (no regex metacharacters) or an actual regex pattern.
func isPlainPhrase(pattern string) bool {
	for _, ch := range pattern {
		if strings.ContainsRune(`*+?[](){}|^$\`, ch) {
			return false
		}
	}
	return true
}

// Evaluate checks text against every rule in cfg.Rules, in order,
// returning the first rule that matches at least one pattern.
// MatchedTerms lists every distinct pattern that matched (not every
// occurrence — a phrase repeated three times still appears once).
// Deterministic, no I/O. An empty/zero Config never triggers.
func Evaluate(text string, cfg Config) Result {
	for _, result := range EvaluateAll(text, cfg) {
		return result
	}
	return Result{}
}

// EvaluateAll returns every matching rule in configuration order. Online
// processing uses Evaluate (first match wins); this richer view powers the
// dry-run impact report without changing online precedence.
func EvaluateAll(text string, cfg Config) []Result {
	var results []Result
	for _, rule := range cfg.Rules {
		if matchesAny(text, rule.ExcludePatterns) {
			continue
		}
		var matched []string
		for _, pattern := range rule.Patterns {
			finalPattern := pattern
			if isPlainPhrase(pattern) {
				finalPattern = `(?i)\b` + regexp.QuoteMeta(pattern) + `\b`
			}
			re, err := regexp.Compile(finalPattern)
			if err == nil && re.MatchString(text) {
				matched = append(matched, pattern)
			}
		}
		if len(matched) > 0 {
			results = append(results, Result{Triggered: true, ReasonCode: rule.ReasonCode, Category: rule.Category, MatchedTerms: matched})
		}
	}
	return results
}

func matchesAny(text string, patterns []string) bool {
	for _, pattern := range patterns {
		finalPattern := pattern
		if isPlainPhrase(pattern) {
			finalPattern = `(?i)\b` + regexp.QuoteMeta(pattern) + `\b`
		}
		if re, err := regexp.Compile(finalPattern); err == nil && re.MatchString(text) {
			return true
		}
	}
	return false
}
