// Package storyguard limits how many messages the channel carries about the
// same story. Near-duplicate scoring and event clustering only catch
// headlines that share word trigrams or an action vocabulary; the same story
// reworded by several outlets ("Burns sells 2.1% stake" vs "Burns sells sliver
// of Isotopes at record valuation") slips through both. This guard compares
// plain word overlap against what was already published or queued.
package storyguard

import (
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

// Policy configures the guard. The zero value (Enabled false) disables it.
type Policy struct {
	Enabled bool
	// Window is how far back published or queued articles count as the same
	// story.
	Window time.Duration
	// MaxPerStory is how many messages one story may have; the next similar
	// article is withheld.
	MaxPerStory int
	// MinSharedTokens is the minimum number of distinct shared title tokens
	// for two titles to be the same story.
	MinSharedTokens int
	// OverlapThreshold is the minimum shared/min(len(a), len(b)) ratio.
	OverlapThreshold float64
	// GenericTokenShare, when above zero, makes Exceeded ignore tokens that
	// appear in more than this share of recent titles ("isotopes", the
	// opponent's name in game week): they say what the channel is about, not
	// which story a title tells. Needs at least minPopulation recent titles
	// to apply.
	GenericTokenShare float64
	// ExemptTitles are titles of series that share a template across
	// different stories ("<player> meets with the media", "<player> arriving
	// at the stadium"); a title matching one is never withheld.
	ExemptTitles []*regexp.Regexp
}

// Exempt reports whether title belongs to a templated series.
func (p Policy) Exempt(title string) bool {
	for _, re := range p.ExemptTitles {
		if re.MatchString(title) {
			return true
		}
	}
	return false
}

const minPopulation = 30

// Recent is an article already published or queued for publication.
type Recent struct {
	ArticleID string
	Title     string
	Tokens    []string
	// ClusterID is set by callers that match against clusters.
	ClusterID string
}

// SameStory reports whether two token sets describe the same story: enough
// distinct shared tokens and a high overlap relative to the shorter title, so
// a short headline contained in a long one still matches.
func (p Policy) SameStory(a, b []string) bool {
	if conflictingNumbers(a, b) {
		return false
	}
	a, b = words(a), words(b)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	shared, smaller := sharedAndSmaller(a, b)
	if shared < p.MinSharedTokens || smaller == 0 {
		return false
	}
	return float64(shared)/float64(smaller) >= p.OverlapThreshold
}

// BestMatch returns the entry of recent that tokens tells the same story as,
// preferring the highest overlap and then the most shared tokens; ok is false
// when none does. Unlike Exceeded it does not require Enabled or MaxPerStory:
// it serves callers (clustering) that only need to find the story.
func (p Policy) BestMatch(tokens []string, recent []Recent) (best Recent, ratio float64, ok bool) {
	generic := genericTokens(recent, p.GenericTokenShare)
	tokens = without(tokens, generic)
	var bestRatio float64
	var bestShared int
	for _, r := range recent {
		other := without(r.Tokens, generic)
		if !p.SameStory(tokens, other) {
			continue
		}
		shared, smaller := sharedAndSmaller(words(tokens), words(other))
		r2 := float64(shared) / float64(smaller)
		if !ok || r2 > bestRatio || (r2 == bestRatio && shared > bestShared) {
			best, bestRatio, bestShared, ok = r, r2, shared, true
		}
	}
	return best, bestRatio, ok
}

func sharedAndSmaller(a, b []string) (shared, smaller int) {
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[t] = true
	}
	seen := make(map[string]bool, len(b))
	for _, t := range b {
		if set[t] && !seen[t] {
			seen[t] = true
			shared++
		}
	}
	return shared, min(len(set), distinctCount(b))
}

// Exceeded reports whether the story of tokens already has MaxPerStory
// messages among recent, and returns the article IDs that matched.
func (p Policy) Exceeded(tokens []string, recent []Recent) (bool, []string) {
	if !p.Enabled || p.MaxPerStory <= 0 {
		return false, nil
	}
	generic := genericTokens(recent, p.GenericTokenShare)
	tokens = without(tokens, generic)
	var matched []string
	for _, r := range recent {
		if p.SameStory(tokens, without(r.Tokens, generic)) {
			matched = append(matched, r.ArticleID)
		}
	}
	return len(matched) >= p.MaxPerStory, matched
}

func distinctCount(tokens []string) int {
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return len(set)
}

// genericTokens returns the tokens present in more than share of recent
// titles, or nil when share is not positive or recent is too small to tell.
func genericTokens(recent []Recent, share float64) map[string]bool {
	if share <= 0 || len(recent) < minPopulation {
		return nil
	}
	df := make(map[string]int)
	for _, r := range recent {
		seen := make(map[string]bool, len(r.Tokens))
		for _, t := range r.Tokens {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	limit := share * float64(len(recent))
	generic := make(map[string]bool)
	for t, n := range df {
		if float64(n) > limit {
			generic[t] = true
		}
	}
	return generic
}

func without(tokens []string, drop map[string]bool) []string {
	if len(drop) == 0 {
		return tokens
	}
	kept := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !drop[t] {
			kept = append(kept, t)
		}
	}
	return kept
}

// numberMark prefixes the number entries Tokenize appends to a title's
// tokens. They are facts to compare (a score, a yardage, a price), not words
// to count, so the overlap ignores them and only conflictingNumbers reads them.
const numberMark = "#n:"

var numberPattern = regexp.MustCompile(`\d+(?:[.,]\d+)?`)

// Tokenize returns the dedup tokens of title followed by its numbers (marked,
// whatever their length: the tokenizer drops short words, which would drop
// "7" and "14" from "7-yard throw, 14-7").
func Tokenize(title string, cfg normalization.TextConfig) []string {
	tokens := normalization.Normalize(title, cfg).Tokens
	seen := map[string]bool{}
	for _, n := range numberPattern.FindAllString(title, -1) {
		n = numberMark + strings.NewReplacer(",", "", ".", "").Replace(n)
		if !seen[n] {
			seen[n] = true
			tokens = append(tokens, n)
		}
	}
	return tokens
}

func isNumber(t string) bool { return strings.HasPrefix(t, numberMark) }

// words returns tokens without the number entries.
func words(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !isNumber(t) {
			out = append(out, t)
		}
	}
	return out
}

// conflictingNumbers reports whether both titles carry numbers and those
// numbers mostly differ (less than half of them in common): "7-yard throw,
// 14-7" and "5-yard throw, 21-7", "over/under 258.5" and "450 yards" are the
// same template reporting different facts, not the same story. A title
// without numbers never conflicts.
func conflictingNumbers(a, b []string) bool {
	setA, setB := map[string]bool{}, map[string]bool{}
	for _, t := range a {
		if isNumber(t) {
			setA[t] = true
		}
	}
	for _, t := range b {
		if isNumber(t) {
			setB[t] = true
		}
	}
	if len(setA) == 0 || len(setB) == 0 {
		return false
	}
	common := 0
	for t := range setA {
		if setB[t] {
			common++
		}
	}
	union := len(setA) + len(setB) - common
	return float64(common)/float64(union) < 0.5
}
