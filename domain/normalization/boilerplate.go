package normalization

import "strings"

// StripPhrases removes every case-insensitive occurrence of each phrase
// from s and collapses the whitespace left behind. It targets templated
// CTA/boilerplate text (e.g. "Subscribe", "Follow us on Facebook") that
// HTML stripping alone can't remove, since it's visible text rather than
// markup.
func StripPhrases(s string, phrases []string) string {
	for _, phrase := range phrases {
		if phrase == "" {
			continue
		}
		s = replaceCaseInsensitive(s, phrase, "")
	}
	return collapseSpace(s)
}

func replaceCaseInsensitive(s, old, new string) string {
	lowerS := strings.ToLower(s)
	lowerOld := strings.ToLower(old)
	var b strings.Builder
	i := 0
	for {
		idx := strings.Index(lowerS[i:], lowerOld)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+idx])
		b.WriteString(new)
		i += idx + len(old)
	}
	return b.String()
}
