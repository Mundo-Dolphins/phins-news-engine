package trainmine

import (
	"sort"
	"strings"
	"unicode"
)

// UnknownTerms extracts the distinct lowercase words (length > 3, to
// skip stopword-sized noise like "the"/"new") appearing across titles
// that are not present in known, sorted alphabetically. This is issue
// #253 part 26's "catalog/dictionary visibility nudge": a report-only
// hint, never an auto-created dictionary/entity proposal — see the
// design spec's "Out of scope". known is built by the caller
// (cmd/train/run.go) from the currently loaded dictionary's
// Entry.CanonicalName/Aliases and the entity catalog's
// EntityRule.Terms, lowercased.
func UnknownTerms(titles []string, known map[string]bool) []string {
	seen := map[string]bool{}
	for _, title := range titles {
		for _, word := range strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) }) {
			w := strings.ToLower(word)
			if len(w) <= 3 || known[w] {
				continue
			}
			seen[w] = true
		}
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}
