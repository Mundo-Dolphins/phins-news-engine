// Package storyguard limits how many messages the channel carries about the
// same story. Near-duplicate scoring and event clustering only catch
// headlines that share word trigrams or an action vocabulary; the same story
// reworded by several outlets ("Ross sells 2.1% stake" vs "Ross sells sliver
// of Dolphins at record valuation") slips through both. This guard compares
// plain word overlap against what was already published or queued.
package storyguard

import (
	"regexp"
	"time"
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
	// appear in more than this share of recent titles ("dolphins", the
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
}

// SameStory reports whether two token sets describe the same story: enough
// distinct shared tokens and a high overlap relative to the shorter title, so
// a short headline contained in a long one still matches.
func (p Policy) SameStory(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[t] = true
	}
	shared, seen := 0, make(map[string]bool, len(b))
	for _, t := range b {
		if set[t] && !seen[t] {
			seen[t] = true
			shared++
		}
	}
	distinctA := len(set)
	distinctB := distinctCount(b)
	smaller := min(distinctA, distinctB)
	if shared < p.MinSharedTokens || smaller == 0 {
		return false
	}
	return float64(shared)/float64(smaller) >= p.OverlapThreshold
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
