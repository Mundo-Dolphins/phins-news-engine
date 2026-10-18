package dictionary

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Occurrence is every appearance of one dictionary entry (its canonical
// name and/or any alias) inside a piece of text, merged into a single
// count so the entry is scored once regardless of how many of its surface
// forms were found.
type Occurrence struct {
	Category    string
	EntryName   string
	MatchedText string
	Count       int
	// Confidence and ActiveUntil are the matched entry's own fields,
	// carried so callers can discount weak or retired entries.
	Confidence  float64
	ActiveUntil *time.Time
}

type termPattern struct {
	term string
	re   *regexp.Regexp
}

type compiledEntry struct {
	canonicalName string
	confidence    float64
	activeUntil   *time.Time
	patterns      []termPattern
}

type compiledCategory struct {
	Category
	entries []compiledEntry
}

// Matcher finds dictionary entries in text using patterns compiled once by
// Prepare — never per-article, since compiling hundreds of patterns on
// every fetched article would be wasteful on constrained hardware.
type Matcher struct {
	categories []compiledCategory
	decision   Decision
	version    string
}

// Prepare compiles every entry's canonical name and aliases in d into a
// Matcher.
func Prepare(d Dictionary) (Matcher, error) {
	categories := make([]compiledCategory, 0, len(d.Categories))
	for _, cat := range d.Categories {
		entries := make([]compiledEntry, 0, len(cat.Entries))
		for _, entry := range cat.Entries {
			terms := append([]string{entry.CanonicalName}, entry.Aliases...)
			patterns := make([]termPattern, 0, len(terms))
			for _, term := range terms {
				re, err := regexp.Compile(boundaryPattern(term))
				if err != nil {
					return Matcher{}, fmt.Errorf("dictionary: compile pattern for %q: %w", term, err)
				}
				patterns = append(patterns, termPattern{term: term, re: re})
			}
			entries = append(entries, compiledEntry{canonicalName: entry.CanonicalName, confidence: entry.Confidence, activeUntil: entry.ActiveUntil, patterns: patterns})
		}
		categories = append(categories, compiledCategory{Category: cat, entries: entries})
	}
	return Matcher{categories: categories, decision: d.Decision, version: d.Version}, nil
}

// boundaryPattern builds a case-insensitive pattern for term, anchoring a
// `\b` word boundary only on sides where term itself starts/ends with a
// word character. A term like "#FinsUp" must not require a boundary before
// "#" — "#" and a preceding space are both non-word characters, so `\b`
// would never match there and the term could never be found at all.
func boundaryPattern(term string) string {
	runes := []rune(term)
	prefix, suffix := "", ""
	if len(runes) > 0 && isWordRune(runes[0]) {
		prefix = `\b`
	}
	if len(runes) > 0 && isWordRune(runes[len(runes)-1]) {
		suffix = `\b`
	}
	return "(?i)" + prefix + regexp.QuoteMeta(term) + suffix
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// Find returns one Occurrence per dictionary entry that appears at least
// once in text, merging matches across the entry's canonical name and all
// its aliases. A single mention of an entry can be matched by more than one
// of its patterns at once — e.g. the canonical name "Ezo Quarrington" and its
// alias "Ezo" both match at the start of "Ezo Quarrington threw...", since
// "Ezo" is a substring of the canonical match. Summing each pattern's match
// count independently would count that one mention twice, so overlapping
// match spans across an entry's patterns are merged before counting:
// non-overlapping spans are separate mentions, overlapping ones are the
// same mention seen through two surface forms.
func (m Matcher) Find(text string) []Occurrence {
	var occurrences []Occurrence
	for _, cat := range m.categories {
		for _, entry := range cat.entries {
			var spans [][2]int
			var matchedForms []string
			for _, tp := range entry.patterns {
				idx := tp.re.FindAllStringIndex(text, -1)
				if len(idx) == 0 {
					continue
				}
				matchedForms = append(matchedForms, tp.term)
				for _, s := range idx {
					spans = append(spans, [2]int{s[0], s[1]})
				}
			}
			count := mergedSpanCount(spans)
			if count == 0 {
				continue
			}
			occurrences = append(occurrences, Occurrence{
				Category: cat.Name, EntryName: entry.canonicalName,
				MatchedText: strings.Join(matchedForms, "; "), Count: count,
				Confidence: entry.confidence, ActiveUntil: entry.activeUntil,
			})
		}
	}
	return occurrences
}

// mergedSpanCount counts the number of distinct, non-overlapping mentions
// represented by spans, where each span is a half-open [start, end) match
// range. Overlapping or touching spans are treated as the same mention.
func mergedSpanCount(spans [][2]int) int {
	if len(spans) == 0 {
		return 0
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i][0] != spans[j][0] {
			return spans[i][0] < spans[j][0]
		}
		return spans[i][1] < spans[j][1]
	})
	count := 0
	end := -1
	for _, s := range spans {
		if count == 0 || s[0] >= end {
			count++
			end = s[1]
		} else if s[1] > end {
			end = s[1]
		}
	}
	return count
}

// Categories returns the prepared dictionary's category policy (name,
// weight, strong-override, requires-any-of) for classification.Decide to
// apply scoring rules against Find's occurrences.
func (m Matcher) Categories() []Category {
	out := make([]Category, len(m.categories))
	for i, c := range m.categories {
		out[i] = c.Category
	}
	return out
}

// Decision returns the threshold policy bundled with the prepared
// dictionary.
func (m Matcher) Decision() Decision { return m.decision }

// Version returns the dictionary version this Matcher was prepared from.
func (m Matcher) Version() string { return m.version }
