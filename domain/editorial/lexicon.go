package editorial

import (
	"regexp"
	"strings"
)

// Lexicon detectors are small, hand-curated keyword/pattern lists — the
// same "keyword list, not a model" concept v1's promoOnlySignals used, not
// its code. No LLM/embeddings per project rules. Each returns a count
// (repeated hits weigh more than a single hit), never negative.
var (
	clickbaitPhrases = []string{
		"you won't believe", "what happened next", "this changes everything",
		"mind blowing", "goes viral", "shocking", "gone wrong", "number will surprise you",
	}
	clickbaitPunctuation = regexp.MustCompile(`[!?]{2,}`)

	promoPhrases = []string{
		"sponsored", "presented by", "shop now", "% off", "promo code",
		"advertisement", "buy now", "limited time offer", "use code",
	}

	rumorPhrases = []string{
		"sources say", "reportedly", "according to sources", "could be",
		"might be", "speculat", "rumor", "expected to", "believed to",
	}

	// "signed" is deliberately absent: even under word-boundary matching,
	// \bsigned\b still matches the tail of "re-signed" (the boundary is
	// the hyphen), so keeping both would double-count a single mention.
	// "re-signed" plus the rest of this list already carries the signal.
	contractualPhrases = []string{
		"contract", "extension", "waived", "released", "re-signed",
		"restructure", "salary cap", "guaranteed money", "incentives",
	}

	dollarAmountPattern = regexp.MustCompile(`\$\s?[\d,.]+\s?(million|billion|k|m)?`)
	percentPattern      = regexp.MustCompile(`\d+(\.\d+)?\s?%`)

	// Word-boundary phrase patterns to prevent substring false positives
	// (e.g., "signed" matching inside "resigned" or "designed").
	clickbaitPatterns   = compilePatterns(clickbaitPhrases)
	promoPatterns       = compilePatterns(promoPhrases)
	rumorPatterns       = compilePatterns(rumorPhrases)
	contractualPatterns = compilePatterns(contractualPhrases)
)

func compilePatterns(phrases []string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, len(phrases))
	for i, phrase := range phrases {
		patterns[i] = regexp.MustCompile(`\b` + regexp.QuoteMeta(phrase) + `\b`)
	}
	return patterns
}

func countPhrases(lower string, patterns []*regexp.Regexp) float64 {
	count := 0.0
	for _, pattern := range patterns {
		count += float64(len(pattern.FindAllString(lower, -1)))
	}
	return count
}

// lexiconFeatures scores text against the five heuristic detectors the
// prompt asks for (clickbait/promo/rumor/contractual/verifiable-data).
// text should be the combined title+body — callers (ExtractFeatures)
// decide what to combine.
func lexiconFeatures(text string) FeatureVector {
	lower := strings.ToLower(text)
	return FeatureVector{
		"lex:clickbait":       countPhrases(lower, clickbaitPatterns) + float64(len(clickbaitPunctuation.FindAllString(text, -1))),
		"lex:promo":           countPhrases(lower, promoPatterns),
		"lex:rumor":           countPhrases(lower, rumorPatterns),
		"lex:contractual":     countPhrases(lower, contractualPatterns),
		"lex:verifiable_data": float64(len(dollarAmountPattern.FindAllString(lower, -1)) + len(percentPattern.FindAllString(lower, -1))),
	}
}
