package dictionaryupdate

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var nameSuffixes = map[string]bool{
	"jr": true, "sr": true, "ii": true, "iii": true, "iv": true,
}

// CanonicalKey produces a loose-match-safe, stable identity key for a
// display name: lowercase, accents stripped, apostrophes/hyphens kept
// (so "Jones-Smith" and "Jonessmith" stay distinguishable, avoiding
// false-positive merges between different people), whitespace collapsed,
// and a trailing generational suffix (Jr./Sr./II/III/IV) dropped — per
// the requirement that suffixes not affect duplicate detection.
func CanonicalKey(name string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	stripped, _, err := transform.String(t, name)
	if err != nil {
		stripped = name
	}
	lower := strings.ToLower(stripped)
	lower = strings.ReplaceAll(lower, "'", "'")

	var b strings.Builder
	prevSpace := false
	for _, r := range lower {
		switch {
		case r == '\'' || r == '-':
			b.WriteRune(r)
			prevSpace = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		case unicode.IsSpace(r):
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		}
	}

	fields := strings.Fields(b.String())
	if n := len(fields); n > 1 && nameSuffixes[fields[n-1]] {
		fields = fields[:n-1]
	}
	return strings.Join(fields, " ")
}

// ClassifyAlias assigns a risk-aware AliasClass to a candidate alias for a
// known canonical display name. A bare, isolated first or last name is
// always Ambiguous — never auto-confirmed — because it is the single
// highest-risk source of false-positive entity matches.
func ClassifyAlias(canonicalDisplayName, alias string) AliasClass {
	canon := CanonicalKey(canonicalDisplayName)
	a := CanonicalKey(alias)

	if a == canon {
		return AliasClassExact
	}

	canonParts := strings.Fields(canon)
	aliasParts := strings.Fields(a)

	if len(aliasParts) == 1 && len(canonParts) >= 2 {
		for _, part := range canonParts {
			if aliasParts[0] == part {
				return AliasClassAmbiguous
			}
		}
	}

	if len(aliasParts) == 2 && len(canonParts) >= 2 {
		first, last := canonParts[0], canonParts[len(canonParts)-1]
		switch {
		case aliasParts[0] == first && aliasParts[1] == last:
			return AliasClassFirstLast
		case len(aliasParts[0]) == 1 && aliasParts[1] == last:
			return AliasClassInitialLast
		}
	}

	if len(a) >= 3 && len(canon) > len(a) && strings.Contains(canon, a) {
		return AliasClassAbbreviated
	}

	if len(aliasParts) == len(canonParts) && len(aliasParts) > 0 {
		if levenshtein(a, canon) <= 2 {
			return AliasClassTypographic
		}
		return AliasClassSuggested
	}

	return AliasClassSuggested
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	m, n := len(ra), len(rb)
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[n]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
