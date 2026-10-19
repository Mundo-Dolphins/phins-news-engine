// Package dictionarymining mines candidate keyword terms from articles a
// human has already labeled `publish` in a review session — bounded by
// construction, never a historical scan.
package dictionarymining

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Candidate is one mined keyword term, not yet persisted or decided.
type Candidate struct {
	Term               string
	CategoryGuess      string // "" if the heuristic had no guess
	SupportCount       int
	FirstSeenArticleID string
}

// FilterConfig removes terms that are operationally unhelpful for the
// team dictionary even when they appear frequently in approved
// articles: dates, generic football/news words, and recurring boilerplate.
type FilterConfig struct {
	// KnownEntityTerms contains catalog canonical names and aliases. Their
	// occurrences are removed before mining, including component words.
	KnownEntityTerms  []string
	MinSupport        int
	IgnoredTerms      []string
	IgnoredTokens     []string
	IgnoredSubstrings []string
	CategoryGuesses   map[string]string
	// CommonWords, when not empty, switches mining to proper nouns only. It
	// holds words the corpus uses in lower case (see CommonWordsFrom): "record",
	// "bye", "taking" are vocabulary, never a team, player or place. Every
	// term must then contain a word that is not common, lower-case tokens are
	// no longer mined, and a phrase made only of common words ("Intriguing
	// Former", "Worst Record") is dropped.
	CommonWords map[string]bool
	// KnownNames are the full names (NameKey form) of people another source
	// already covers, such as the nflverse roster: a candidate that is one of
	// them, or that contains one, is not proposed again. KnownSurnames does the
	// same for a single capitalized word.
	KnownNames    map[string]bool
	KnownSurnames map[string]bool
	// EnglishWords is a dictionary of ordinary English words (any inflection).
	// With it, lower-case tokens are mined again, but only the ones that are not
	// English: nicknames, slang and coined words ("phins"), which no corpus
	// statistic can tell from vocabulary.
	EnglishWords map[string]bool
}

var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "v": true}

// NameKey folds a person's name to the form KnownNames uses: lower case, curly
// apostrophes and periods removed, single spaces, no generational suffix
// ("Marvin Harrison Jr." -> "marvin harrison").
func NameKey(name string) string {
	name = strings.ToLower(normalizeMiningText(name))
	name = strings.NewReplacer(".", "", ",", " ").Replace(name)
	words := strings.Fields(name)
	for len(words) > 1 && nameSuffixes[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// coversKnownName reports whether term is, or contains as consecutive words, a
// known full name, or is a single known surname.
func coversKnownName(term string, cfg FilterConfig) bool {
	words := strings.Fields(NameKey(term))
	if len(words) == 0 {
		return false
	}
	if len(words) == 1 {
		return cfg.KnownSurnames[words[0]]
	}
	for i := 0; i < len(words); i++ {
		for j := i + 2; j <= len(words); j++ {
			if cfg.KnownNames[strings.Join(words[i:j], " ")] {
				return true
			}
		}
	}
	return false
}

// capitalizedPhrasePattern matches a two-to-three-word Title Case phrase
// (e.g. "Ezo Quarrington") — the same shape v1's person-name heuristic
// used, guessed into nfl_people_context (the existing low-weight,
// bulk, unverified-person-mention category), never a new high-weight
// category.
var capitalizedPhrasePattern = regexp.MustCompile(`\b[A-Z][\p{L}]*(?:['&-][\p{L}]+)*(?:(?:\s+|\s*&\s*)[A-Z][\p{L}]*(?:['&-][\p{L}]+)*){1,2}\b`)

// Keep internal punctuation in mining tokens without changing the shared
// normalization used for deduplication and classifier features.
var miningTokenPattern = regexp.MustCompile(`[\p{L}\p{N}]+(?:['&-][\p{L}\p{N}]+)*(?:\s*&\s*[\p{L}\p{N}]+(?:['&-][\p{L}\p{N}]+)*)*`)

func normalizeMiningText(text string) string {
	return strings.NewReplacer("’", "'", "‘", "'", "‐", "-", "‑", "-").Replace(text)
}

// knownEntitySegments removes whole catalog mentions, retaining a boundary
// between the remaining pieces so bigrams never bridge a removed entity.
func knownEntitySegments(text string, patterns []*regexp.Regexp) []string {
	if len(patterns) == 0 {
		return []string{text}
	}
	masked := make([]bool, len(text))
	for _, pattern := range patterns {
		for _, span := range pattern.FindAllStringIndex(text, -1) {
			if span[0] > 0 {
				r, _ := utf8.DecodeLastRuneInString(text[:span[0]])
				if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
					continue
				}
			}
			if span[1] < len(text) {
				r, _ := utf8.DecodeRuneInString(text[span[1]:])
				if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
					continue
				}
			}
			for i := span[0]; i < span[1]; i++ {
				masked[i] = true
			}
		}
	}
	var segments []string
	start := 0
	for i := 0; i < len(text); i++ {
		if !masked[i] {
			continue
		}
		if start < i {
			segments = append(segments, text[start:i])
		}
		for i+1 < len(text) && masked[i+1] {
			i++
		}
		start = i + 1
	}
	if start < len(text) {
		segments = append(segments, text[start:])
	}
	return segments
}

func miningTokens(text string, cfg normalization.TextConfig) []string {
	// Prevent dedup's ampersand-to-"and" expansion for this mining view.
	text = strings.ReplaceAll(text, " & ", " &")
	dedup := strings.ReplaceAll(normalization.Normalize(text, cfg).ForDedup, " &", " & ")
	stop := make(map[string]bool, len(cfg.StopWords))
	for _, word := range cfg.StopWords {
		stop[strings.ToLower(word)] = true
	}
	var tokens []string
	for _, token := range miningTokenPattern.FindAllString(dedup, -1) {
		token = strings.Join(strings.Fields(token), " ")
		if len(token) >= cfg.MinTokenLength && !stop[token] {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

var timeLikePattern = regexp.MustCompile(`^\d{1,2}(?::\d{2})?(?:am|pm)$`)

// candidateAgg accumulates one candidate's first-seen display form,
// support count, and category guess across every article scanned.
type candidateAgg struct {
	term          string
	categoryGuess string
	count         int
	firstArticle  string
}

// ExtractCandidates mines Candidate terms from articles (already filtered
// to one session's publish-labeled set — see Select), excluding anything
// matcher already covers. It has no I/O and no dependency on any port.
//
// Two independent pools are mined per article's Title+Summary: Title Case
// two-to-three-word phrases from cleaned text (guessed as
// nfl_people_context), and generic single tokens plus adjacent-token
// bigrams from normalization.Normalize's already-lowercased,
// stopword-filtered mining tokens that preserve internal punctuation (no
// category guess). Because tokens have stopwords already removed, an
// adjacent-token bigram does not always
// correspond to two words that were literally adjacent in the source text
// (e.g. "quarterback of the year" tokenizes to ["quarterback","year"],
// producing the bigram "quarterback year") — an accepted heuristic
// looseness, since every candidate still passes through human
// approve/reject before ever reaching the dictionary file.
func ExtractCandidates(articles []article.Article, textCfg normalization.TextConfig, matcher dictionary.Matcher) []Candidate {
	return ExtractCandidatesWithFilter(articles, textCfg, matcher, FilterConfig{})
}

// ExtractCandidatesWithFilter mines Candidate terms like ExtractCandidates,
// then applies FilterConfig before sorting. The filtering is deliberately
// deterministic and conservative: it suppresses known-common noise without
// trying to infer whether a one-word unknown is a surname, since that would
// hide real staff/player additions.
func ExtractCandidatesWithFilter(articles []article.Article, textCfg normalization.TextConfig, matcher dictionary.Matcher, filter FilterConfig) []Candidate {
	var entityPatterns []*regexp.Regexp
	compiledTerms := make(map[string]bool)
	for _, term := range filter.KnownEntityTerms {
		term = normalizeMiningText(strings.Join(strings.Fields(term), " "))
		key := strings.ToLower(term)
		if key != "" && !compiledTerms[key] {
			compiledTerms[key] = true
			entityPatterns = append(entityPatterns, regexp.MustCompile("(?i)"+regexp.QuoteMeta(term)))
		}
	}
	seen := map[string]*candidateAgg{}
	var order []string

	record := func(term, categoryGuess, articleID string) {
		key := strings.ToLower(term)
		a, ok := seen[key]
		if !ok {
			a = &candidateAgg{term: term, categoryGuess: categoryGuess, firstArticle: articleID}
			seen[key] = a
			order = append(order, key)
		}
		a.count++
	}

	for _, a := range articles {
		text := normalizeMiningText(normalization.Normalize(a.Title+" "+a.Summary, textCfg).ForClassification)
		for _, text := range knownEntitySegments(text, entityPatterns) {
			if len(filter.CommonWords) > 0 {
				for _, term := range properNounTerms(text, textCfg, filter.CommonWords) {
					if len(matcher.Find(term)) > 0 || coversKnownName(term, filter) {
						continue
					}
					guess := ""
					if strings.Contains(term, " ") {
						guess = "nfl_people_context"
					}
					record(term, guess, a.ID)
				}
				if len(filter.EnglishWords) > 0 && !looksNonEnglish(text) {
					for _, tok := range lowerCaseTokens(text) {
						if !isCoinedWord(tok, filter) || len(matcher.Find(tok)) > 0 {
							continue
						}
						record(tok, "", a.ID)
					}
				}
				continue
			}
			for _, phrase := range capitalizedPhrasePattern.FindAllString(text, -1) {
				if len(matcher.Find(phrase)) > 0 {
					continue
				}
				record(phrase, "nfl_people_context", a.ID)
			}

			tokens := miningTokens(text, textCfg)
			for _, tok := range tokens {
				if len(matcher.Find(tok)) > 0 {
					continue
				}
				record(tok, "", a.ID)
			}
			for i := 0; i+1 < len(tokens); i++ {
				bigram := tokens[i] + " " + tokens[i+1]
				if len(matcher.Find(bigram)) > 0 {
					continue
				}
				record(bigram, "", a.ID)
			}
		}
	}
	candidates := make([]Candidate, 0, len(order))
	for _, key := range order {
		a := seen[key]
		if !filter.Allows(a.term, a.count) {
			continue
		}
		if guess := categoryGuessFor(a.term, filter); guess != "" {
			a.categoryGuess = guess
		}
		candidates = append(candidates, Candidate{
			Term: a.term, CategoryGuess: a.categoryGuess, SupportCount: a.count, FirstSeenArticleID: a.firstArticle,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].SupportCount > candidates[j].SupportCount })
	return candidates
}

// Allows reports whether a term satisfies the mining policy. Review uses
// the same policy for candidates persisted before exclusions were updated.
func (cfg FilterConfig) Allows(term string, support int) bool {
	return !shouldFilter(term, support, cfg)
}

func shouldFilter(term string, support int, cfg FilterConfig) bool {
	if cfg.MinSupport > 0 && support < cfg.MinSupport {
		return true
	}
	key := normalizeFilterTerm(term)
	if key == "" || timeLikePattern.MatchString(key) || containsNumericToken(key) || isAllCapsPhrase(term) {
		return true
	}
	ignored := make(map[string]bool, len(cfg.IgnoredTerms))
	for _, term := range cfg.IgnoredTerms {
		ignored[normalizeFilterTerm(term)] = true
	}
	if ignored[key] {
		return true
	}
	ignoredTokens := make(map[string]bool, len(cfg.IgnoredTokens))
	for _, tok := range cfg.IgnoredTokens {
		ignoredTokens[normalizeFilterTerm(tok)] = true
	}
	for _, tok := range strings.Fields(key) {
		if ignoredTokens[tok] {
			return true
		}
	}
	for _, needle := range cfg.IgnoredSubstrings {
		needle = normalizeFilterTerm(needle)
		if needle != "" && strings.Contains(key, needle) {
			return true
		}
	}
	return false
}

func normalizeFilterTerm(term string) string {
	return strings.Join(strings.Fields(strings.ToLower(term)), " ")
}

func containsNumericToken(term string) bool {
	for _, tok := range strings.Fields(term) {
		allDigits := true
		for _, r := range tok {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return true
		}
	}
	return false
}

func isAllCapsPhrase(term string) bool {
	if !strings.Contains(term, " ") {
		return false
	}
	hasLetter := false
	for _, r := range term {
		if r >= 'a' && r <= 'z' {
			return false
		}
		if r >= 'A' && r <= 'Z' {
			hasLetter = true
		}
	}
	return hasLetter
}

func categoryGuessFor(term string, cfg FilterConfig) string {
	if len(cfg.CategoryGuesses) == 0 {
		return ""
	}
	return cfg.CategoryGuesses[normalizeFilterTerm(term)]
}

// Deps are the ports Select reads from.
type Deps struct {
	ReviewDecisions out.ReviewDecisionRepository
	Articles        out.ArticleRepository
}

// Select mines Candidate terms from sessionID's publish-labeled articles
// (both a plain "publish" and an "update" action resolve to Label ==
// "publish" — see domain/reviewdecision), excluding anything matcher
// already covers. Read-only: persistence is the caller's responsibility
// (out.DictionaryCandidateRepository).
func Select(ctx context.Context, deps Deps, sessionID string, matcher dictionary.Matcher, textCfg normalization.TextConfig, filter FilterConfig) ([]Candidate, error) {
	decisions, err := deps.ReviewDecisions.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("dictionarymining: list session decisions: %w", err)
	}

	var articles []article.Article
	for _, d := range decisions {
		if d.Label != "publish" {
			continue
		}
		a, findErr := deps.Articles.FindByID(ctx, d.ArticleID)
		if findErr != nil {
			continue // an orphaned decision shouldn't fail mining — mirrors train export's own skip-on-missing-article behavior
		}
		articles = append(articles, a)
	}

	if filter.CommonWords == nil {
		// Best effort: without a background corpus mining keeps its old
		// behavior rather than failing.
		if background, bgErr := deps.Articles.ListCandidatesForReview(ctx, time.Now().AddDate(0, 0, -backgroundDays), backgroundLimit); bgErr == nil && len(background) >= minBackground {
			filter.CommonWords = CommonWordsFrom(append(background, articles...), commonMinLower, commonLowerShare)
		}
	}
	return ExtractCandidatesWithFilter(articles, textCfg, matcher, filter), nil
}

// The background corpus that decides which words are common vocabulary.
const (
	backgroundDays  = 7
	backgroundLimit = 4000
	// Below this many articles a word cannot be judged common or proper.
	minBackground    = 500
	commonMinLower   = 3
	commonLowerShare = 0.3
)

var lowerWordPattern = regexp.MustCompile(`[\p{L}][\p{L}']*`)

// commonWordKey folds a word to the key CommonWords uses.
func commonWordKey(word string) string {
	return strings.ToLower(strings.Trim(normalizeMiningText(word), "'"))
}

// CommonWordsFrom returns the words that articles use as ordinary vocabulary:
// those written in lower case at least minLower times, and in lower case at
// least lowerShare of all their occurrences. Proper nouns are (almost) never
// written in lower case, headlines in Title Case do not make a word proper,
// and a few lower-case tweets do not make a surname common, hence both
// thresholds. Pass a corpus much larger than the sample being mined (a week
// of articles): a word seen twice cannot be judged.
func CommonWordsFrom(articles []article.Article, minLower int, lowerShare float64) map[string]bool {
	lower, total := map[string]int{}, map[string]int{}
	for _, a := range articles {
		text := normalizeMiningText(a.Title + " " + a.Summary)
		for _, word := range lowerWordPattern.FindAllString(text, -1) {
			key := commonWordKey(word)
			if len(key) < 2 {
				continue
			}
			total[key]++
			if first, _ := utf8.DecodeRuneInString(word); unicode.IsLower(first) {
				lower[key]++
			}
		}
	}
	common := make(map[string]bool)
	for key, n := range lower {
		if n >= minLower && float64(n) >= lowerShare*float64(total[key]) {
			common[key] = true
		}
	}
	return common
}

// hasProperWord reports whether term contains a word that is not common
// vocabulary.
func hasProperWord(term string, common map[string]bool) bool {
	for _, word := range strings.Fields(term) {
		if !common[commonWordKey(word)] {
			return true
		}
	}
	return false
}

// capitalizedRunPattern matches a run of consecutive Capitalized words.
var capitalizedRunPattern = regexp.MustCompile(`\b[A-Z][\p{L}]*(?:'[\p{L}]+)*(?:\s+[A-Z][\p{L}]*(?:'[\p{L}]+)*)*`)

// properNounTerms lists, for one text, the proper-noun candidates: each run of
// capitalized words with the stopwords (The, And, Of...) trimmed from its ends,
// a run of one word as that word, a longer run as its phrase (windows of up to
// three words), and in both cases only if it holds a word that is not common
// vocabulary. A single word inside a longer run is not a candidate on its own:
// "Moe" and "Angeles" are halves of "Moe Szyslak" and "Los Angeles".
func properNounTerms(text string, cfg normalization.TextConfig, common map[string]bool) []string {
	stop := make(map[string]bool, len(cfg.StopWords))
	for _, w := range cfg.StopWords {
		stop[strings.ToLower(w)] = true
	}
	// edge reports whether a word at either end of a run is filler rather than
	// part of a name: a stopword, or vocabulary the corpus writes in lower case.
	edge := func(word string) bool {
		key := commonWordKey(word)
		return stop[key] || common[key] || calendarWords[key]
	}
	trim := func(words []string) []string {
		cleaned := make([]string, 0, len(words))
		for _, w := range words {
			if w = cleanWord(w); w != "" {
				cleaned = append(cleaned, w)
			}
		}
		words = cleaned
		for len(words) > 1 && edge(words[0]) {
			words = words[1:]
		}
		for len(words) > 1 && edge(words[len(words)-1]) {
			words = words[:len(words)-1]
		}
		if len(words) == 1 && (stop[commonWordKey(words[0])] || calendarWords[commonWordKey(words[0])]) {
			return nil
		}
		return words
	}
	var terms []string
	for _, run := range capitalizedRunPattern.FindAllString(text, -1) {
		words := trim(strings.Fields(run))
		switch {
		case len(words) == 0:
		case len(words) == 1:
			if len([]rune(words[0])) >= 3 && !isHandleLike(words[0]) && hasProperWord(words[0], common) {
				terms = append(terms, words[0])
			}
		default:
			for i := 0; i < len(words); i += 3 {
				end := i + 3
				if end > len(words) {
					end = len(words)
				}
				if end-i < 2 {
					break
				}
				phrase := strings.Join(words[i:end], " ")
				if hasProperWord(phrase, common) {
					terms = append(terms, phrase)
				}
			}
		}
	}
	return terms
}

// calendarWords are capitalized by spelling, so the corpus never shows them in
// lower case, yet they are never entities.
var calendarWords = map[string]bool{
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true,
	"january": true, "february": true, "march": true, "april": true, "may": true, "june": true, "july": true,
	"august": true, "september": true, "october": true, "november": true, "december": true,
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true, "jul": true, "aug": true, "sep": true, "sept": true, "oct": true, "nov": true, "dec": true,
}

var contractionSuffix = regexp.MustCompile(`(?i)'(m|ll|ve|re|d|t|s)$`)

// cleanWord drops a possessive ("Springfield's" -> "Springfield") and returns "" for a
// contraction ("I'm", "Don't").
func cleanWord(word string) string {
	loc := contractionSuffix.FindStringSubmatchIndex(word)
	if loc == nil {
		return word
	}
	if strings.EqualFold(word[loc[2]:loc[3]], "s") {
		return word[:loc[0]]
	}
	return ""
}

// isCoinedWord reports whether a lower-case token looks like a nickname or
// coined word: alphabetic, not an English word, not corpus vocabulary and not
// a known name.
func isCoinedWord(tok string, cfg FilterConfig) bool {
	if len([]rune(tok)) < 4 {
		return false
	}
	for _, r := range tok {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	key := commonWordKey(tok)
	if cfg.CommonWords[key] || cfg.KnownSurnames[key] || englishForm(key, cfg.EnglishWords) {
		return false
	}
	return true
}

// inflectionSuffixes are the endings englishForm strips to find a base word.
var inflectionSuffixes = []string{"ing", "ed", "es", "s", "ly", "er", "est", "d", "ers", "ness", "ment", "ments", "ings"}

// englishForm reports whether word is in words or is an inflection or simple
// derivation of a word in it. A word list holds base forms ("hire"), not
// "hiring", "hired" or "hires".
func englishForm(word string, words map[string]bool) bool {
	if words[word] {
		return true
	}
	for _, suffix := range inflectionSuffixes {
		if !strings.HasSuffix(word, suffix) || len(word) <= len(suffix)+2 {
			continue
		}
		stem := strings.TrimSuffix(word, suffix)
		if words[stem] || words[stem+"e"] || words[stem+"y"] {
			return true
		}
		if n := len(stem); n > 2 && stem[n-1] == stem[n-2] && words[stem[:n-1]] {
			return true // doubled consonant: stopping -> stop
		}
		if strings.HasSuffix(stem, "i") && words[strings.TrimSuffix(stem, "i")+"y"] {
			return true // tries -> try
		}
	}
	return false
}

// lowerCaseTokens returns the words of text that are written entirely in lower
// case there. Names, which Title Case headlines and normalization would also
// fold to lower case ("cobee", "jaylen"), are left out: only a word the author
// really wrote in lower case can be slang or a nickname.
func lowerCaseTokens(text string) []string {
	var out []string
	for _, tok := range lowerWordPattern.FindAllString(text, -1) {
		if strings.ToLower(tok) == tok && !strings.Contains(tok, "'") {
			out = append(out, tok)
		}
	}
	return out
}

var spanishFunctionWords = map[string]bool{
	"el": true, "la": true, "los": true, "las": true, "de": true, "del": true, "que": true, "con": true,
	"por": true, "para": true, "una": true, "un": true, "en": true, "se": true, "su": true, "al": true, "como": true,
}

// looksNonEnglish reports whether text reads as Spanish (two or more function
// words), so its words are not mistaken for English slang.
func looksNonEnglish(text string) bool {
	n := 0
	for _, w := range strings.Fields(strings.ToLower(text)) {
		if spanishFunctionWords[strings.Trim(w, ".,;:!?¡¿\"()")] {
			n++
			if n >= 2 {
				return true
			}
		}
	}
	return false
}

var camelCasePattern = regexp.MustCompile(`[a-z][A-Z]`)

// isHandleLike reports whether word looks like a social handle or brand
// (NFLTradeRumors, TylerCarmona), which is not a dictionary term.
func isHandleLike(word string) bool { return camelCasePattern.MatchString(word) }
