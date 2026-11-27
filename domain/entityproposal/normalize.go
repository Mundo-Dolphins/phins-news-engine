package entityproposal

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Normalize produces the aggregation key Discover uses to collapse
// typographic variants of the same name into one proposal: accents
// folded, lowercased, every non-letter/non-digit character (apostrophes,
// hyphens, punctuation) dropped rather than kept, and whitespace
// collapsed. This is deliberately looser than
// dictionaryupdate.CanonicalKey (which keeps apostrophes/hyphens to avoid
// merging distinct surnames) because this package's job is the opposite:
// merging "Jai'Rene' Mossbank" and "JaiRene Mossbank" into a single
// candidate, per the issue's own example.
func Normalize(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, s)
	if err != nil {
		folded = s
	}
	folded = strings.ToLower(folded)

	var b strings.Builder
	prevSpace := true // true at start so leading whitespace is dropped, not turned into a leading space
	for _, r := range folded {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		case unicode.IsSpace(r):
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
		default:
			// punctuation/apostrophes/hyphens: dropped entirely, not
			// turned into a space, so "Mossbank" and "Mc-Millan" also
			// normalize identically.
		}
	}
	return strings.TrimSpace(b.String())
}

// generationalSuffixes are trailing tokens that identify a generation,
// not a surname. They are stripped by Surname so that "Ollie Gordon II"
// yields surname "gordon" rather than "ii".
var generationalSuffixes = map[string]bool{
	"jr": true, "sr": true, "ii": true, "iii": true, "iv": true,
}

// Surname returns the normalized last whitespace-separated token of
// canonicalName, or "" when the name has fewer than two parts (a single
// token has no meaningful surname to key alias/duplicate detection on).
// Trailing generational suffixes (Jr, Sr, II, III, IV) are stripped so
// that "Ollie Gordon II" yields "gordon", not "ii".
func Surname(canonicalName string) string {
	parts := strings.Fields(Normalize(canonicalName))
	for len(parts) > 1 && generationalSuffixes[parts[len(parts)-1]] {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}
