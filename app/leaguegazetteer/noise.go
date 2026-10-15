// app/leaguegazetteer/noise.go
package leaguegazetteer

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsNoise reports whether a mention is not a plausible entity name, using
// format rules only (no evidence): handles, URLs, digits-first tokens, too few
// letters, long runs of one character. minLetters <= 0 means 3.
func IsNoise(mention string, minLetters int) (bool, string) {
	if minLetters <= 0 {
		minLetters = 3
	}
	m := strings.TrimSpace(mention)
	if m == "" {
		return true, "empty"
	}
	if strings.ContainsAny(m, "@_/") || strings.Contains(m, "://") {
		return true, "handle or url"
	}
	letters, run := 0, 0
	var prev rune
	for _, r := range m {
		if unicode.IsLetter(r) {
			letters++
		}
		if r == prev {
			run++
			if run >= 4 && unicode.IsLetter(r) {
				return true, "repeated character"
			}
		} else {
			run = 1
		}
		prev = r
	}
	if letters == 0 {
		return true, "no letters"
	}
	if letters < minLetters {
		return true, "too few letters"
	}
	if first, _ := utf8.DecodeRuneInString(m); unicode.IsDigit(first) && !strings.ContainsAny(m, " ") && utf8.RuneCountInString(m) > 5 {
		return true, "handle-like digit prefix"
	}
	return false, ""
}
