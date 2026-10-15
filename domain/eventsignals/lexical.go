package eventsignals

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenClass is the lexical class of one whitespace-separated token as seen
// by the unknown-entity detector.
type tokenClass int

const (
	// tokenNotName: not part of any name span; always breaks a run.
	tokenNotName tokenClass = iota
	// tokenNameWord: a Title-Case word ("Ashcombe", "Da'Rel", "Pemberly-Quist").
	tokenNameWord
	// tokenInitials: dotted initials ("A.J.", "T.J.").
	tokenInitials
	// tokenSuffix: a generational suffix ("Jr.", "Sr.", "II".."V"); only
	// joins a run that already holds a plausible name.
	tokenSuffix
	// tokenContraction: an English contraction ("I'm", "Don't"); it breaks
	// the run and poisons the rest of its capitalized stretch.
	tokenContraction
)

// punctReplacer folds typographic apostrophes and hyphens into their ASCII
// forms so source text, catalog terms and name spans share one spelling.
var punctReplacer = strings.NewReplacer(
	"’", "'", "‘", "'", "ʼ", "'", "`", "'",
	"‐", "-", "‑", "-",
)

func canonPunct(s string) string { return punctReplacer.Replace(s) }

// matchKey is the comparison key for known/stoplisted lookups: lower-cased,
// punctuation-canonicalized, dots dropped so "Q.T. Brindlewood" == "QT Brindlewood".
func matchKey(s string) string {
	return strings.ReplaceAll(strings.ToLower(canonPunct(s)), ".", "")
}

var (
	initialsRe = regexp.MustCompile(`^(?:\p{Lu}\.){2,}`)
	suffixes   = map[string]bool{"Jr": true, "Sr": true, "II": true, "III": true, "IV": true, "V": true}
	// contractionTails are the word endings after the last apostrophe that
	// mark an English contraction rather than a name like D'Ambry.
	contractionTails = map[string]bool{"t": true, "re": true, "ve": true, "ll": true, "d": true, "m": true}
	// possessiveStopStems: stems for which 's is a contraction ("He's").
	possessiveStopStems = map[string]bool{"he": true, "she": true, "it": true, "that": true, "there": true, "here": true, "what": true, "who": true, "let": true, "where": true, "how": true, "when": true, "why": true}
)

const trimChars = ".,:;!?\"'()[]{}“”‘’"

// classifyToken classifies raw and returns the (cleaned) token text and
// whether it terminates the current run (trailing . , ; : ! ? or closing).
func classifyToken(raw string) (text string, class tokenClass, endsRun bool) {
	raw = canonPunct(raw)
	trimmedLeft := strings.TrimLeft(raw, "\"'([{“”")

	if m := initialsRe.FindString(trimmedLeft); m != "" {
		rest := trimmedLeft[len(m):]
		if rest == "" || strings.Trim(rest, ",;:!?\")]}'") == "" {
			return m, tokenInitials, rest != "" && hasTrailingTerminator(rest)
		}
	}

	trimmed := strings.Trim(raw, trimChars)
	if suffixes[trimmed] && len(trimmed) >= 1 {
		// "Jr." keeps its dot in the span text.
		if strings.HasSuffix(strings.TrimRight(raw, "\")]}'"), ".") && (trimmed == "Jr" || trimmed == "Sr") {
			return trimmed + ".", tokenSuffix, true
		}
		return trimmed, tokenSuffix, hasTrailingTerminator(raw)
	}
	if trimmed == "St" && strings.HasSuffix(raw, ".") {
		return "St.", tokenNameWord, false
	}

	if utf8.RuneCountInString(trimmed) < 2 {
		return "", tokenNotName, false
	}
	first, size := utf8.DecodeRuneInString(trimmed)
	if !unicode.IsUpper(first) {
		return "", tokenNotName, false
	}
	rest := trimmed[size:]
	if rest == strings.ToUpper(rest) && utf8.RuneCountInString(trimmed) <= 4 && !strings.Contains(rest, "'") {
		return "", tokenNotName, false
	}
	for _, r := range rest {
		if !unicode.IsLetter(r) && r != '\'' && r != '-' {
			return "", tokenNotName, false
		}
	}
	if strings.HasPrefix(rest, "-") || strings.HasSuffix(rest, "-") || strings.Contains(trimmed, "--") {
		return "", tokenNotName, false
	}

	end := hasTrailingTerminator(raw)
	if i := strings.LastIndex(trimmed, "'"); i >= 0 {
		stem, tail := trimmed[:i], strings.ToLower(trimmed[i+1:])
		switch {
		case tail == "":
			return "", tokenNotName, false
		case contractionTails[tail]:
			return trimmed, tokenContraction, end
		case tail == "s":
			if utf8.RuneCountInString(stem) < 3 || possessiveStopStems[strings.ToLower(stem)] {
				return trimmed, tokenContraction, end
			}
			// Possessive: keep the stem only.
			return stem, tokenNameWord, end
		case utf8.RuneCountInString(stem) == 1 && stem == strings.ToUpper(stem) && strings.ToLower(stem) == "i":
			return trimmed, tokenContraction, end
		}
	}
	return trimmed, tokenNameWord, end
}

func hasTrailingTerminator(raw string) bool {
	trimmedRight := strings.TrimRight(raw, "\"')]}”’")
	if trimmedRight == "" {
		return false
	}
	return strings.ContainsRune(".,;:!?", rune(trimmedRight[len(trimmedRight)-1]))
}

// originalSpelling maps name — a substring of canonPunct(raw) — back to the
// source spelling in raw (typographic apostrophes/hyphens preserved). The
// canonicalization is rune-for-rune, so rune offsets carry over directly.
func originalSpelling(raw, name string) string {
	if name == "" {
		return name
	}
	canon := canonPunct(raw)
	idx := strings.Index(canon, name)
	if idx < 0 {
		return name
	}
	start := utf8.RuneCountInString(canon[:idx])
	n := utf8.RuneCountInString(name)
	runes := []rune(raw)
	if start+n > len(runes) {
		return name
	}
	return string(runes[start : start+n])
}
