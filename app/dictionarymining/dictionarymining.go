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

	return ExtractCandidatesWithFilter(articles, textCfg, matcher, filter), nil
}
