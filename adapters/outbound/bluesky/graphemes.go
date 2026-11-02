package bluesky

import "unicode"

// countGraphemes returns the number of user-perceived characters in s, the
// unit of Bluesky's 300-character post limit. The module carries no Unicode
// segmentation dependency, so this implements the extended-grapheme rules
// that matter for post text: combining marks, variation selectors, emoji
// modifiers, zero-width-joiner sequences, regional-indicator flag pairs,
// emoji tag sequences, keycaps and CR LF. Hangul syllable sequences are not
// modelled; precomposed syllables count as one each, as the platform does.
func countGraphemes(s string) int {
	count := 0
	var prev rune
	riRun := 0      // regional indicators in the current run
	joined := false // previous rune was a ZWJ, so the next rune extends the cluster
	for i, r := range s {
		extends := false
		switch {
		case i == 0:
		case prev == '\r' && r == '\n':
			extends = true
		case joined:
			extends = true
		case isExtender(r):
			extends = true
		case isRegionalIndicator(r) && isRegionalIndicator(prev) && riRun%2 == 1:
			extends = true
		}
		if !extends {
			count++
		}
		if isRegionalIndicator(r) {
			if extends || riRun == 0 {
				riRun++
			} else {
				riRun = 1
			}
		} else {
			riRun = 0
		}
		joined = r == 0x200D
		prev = r
	}
	return count
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isExtender(r rune) bool {
	switch {
	case r == 0x200D: // zero width joiner
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF: // emoji skin-tone modifiers
		return true
	case r >= 0xE0020 && r <= 0xE007F: // emoji tag characters
		return true
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc)
}
