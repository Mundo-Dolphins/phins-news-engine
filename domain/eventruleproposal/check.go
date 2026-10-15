// domain/eventruleproposal/check.go
package eventruleproposal

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// singleTokenMinChars is the minimum length of a lone, non-stopword token.
const singleTokenMinChars = 8

// ReasonNoMatches is the Check reason for a phrase that changes nothing in
// the sample. Callers may treat it as "no evidence yet" rather than a defect.
const ReasonNoMatches = "no matches in the sample"

// CheckOptions tunes the deterministic gate.
type CheckOptions struct {
	MinChars  int
	MinTokens int
	// MaxTokens rejects longer phrases (0 means no limit): a long phrase
	// is a sentence fragment that will rarely match again.
	MaxTokens        int
	MaxClusterSpread float64
	Stopwords        map[string]bool
}

// DefaultStopwords are the generic words DefaultCheckOptions never lets a
// phrase consist of.
var DefaultStopwords = []string{"said", "says", "team", "the", "nfl", "game", "report", "news"}

// TeamStopwords are extra stopwords naming the team the host program
// covers (e.g. its city and nickname, lowercase): a phrase made of them
// says nothing about the event. Empty by default; a host program sets it
// once at startup.
var TeamStopwords []string

// DefaultCheckOptions returns the production defaults: DefaultStopwords
// plus TeamStopwords.
func DefaultCheckOptions() CheckOptions {
	sw := make(map[string]bool, len(DefaultStopwords)+len(TeamStopwords))
	for _, w := range DefaultStopwords {
		sw[w] = true
	}
	for _, w := range TeamStopwords {
		sw[strings.ToLower(strings.TrimSpace(w))] = true
	}
	return CheckOptions{MinChars: 6, MinTokens: 2, MaxTokens: 6, MaxClusterSpread: 0.5, Stopwords: sw}
}

// Verdict is the outcome of Check.
type Verdict struct {
	OK      bool
	Reasons []string
}

// Check applies the deterministic rules to a proposal and its impact.
// Type proposals skip the phrase and impact rules.
func Check(p Proposal, impact ImpactResult, totalSample int, opt CheckOptions) Verdict {
	if p.Kind == KindType {
		return Verdict{OK: true}
	}
	var reasons []string
	if impact.InVocabulary != "" {
		reasons = append(reasons, "already in the rule vocabulary ("+impact.InVocabulary+")")
	}
	if impact.Retyped > 0 {
		reasons = append(reasons, fmt.Sprintf("retypes %d already-typed articles", impact.Retyped))
	}
	phrase := strings.ToLower(strings.TrimSpace(p.Phrase))
	tokens := strings.Fields(phrase)
	if utf8.RuneCountInString(phrase) < opt.MinChars {
		reasons = append(reasons, fmt.Sprintf("phrase shorter than %d characters", opt.MinChars))
	}
	if len(tokens) < opt.MinTokens {
		single := len(tokens) == 1 && !opt.Stopwords[tokens[0]] &&
			utf8.RuneCountInString(tokens[0]) >= singleTokenMinChars
		if !single {
			reasons = append(reasons, fmt.Sprintf("phrase has fewer than %d tokens and is not a distinctive single word", opt.MinTokens))
		}
	}
	if opt.MaxTokens > 0 && len(tokens) > opt.MaxTokens {
		reasons = append(reasons, fmt.Sprintf("phrase has more than %d tokens", opt.MaxTokens))
	}
	if len(tokens) > 1 && allStopwords(tokens, opt.Stopwords) {
		reasons = append(reasons, "phrase is made only of stopwords")
	}
	if impact.Matches == 0 {
		reasons = append(reasons, ReasonNoMatches)
	} else if totalSample > 0 && impact.DistinctClusters >= 3 &&
		float64(impact.Matches)/float64(totalSample) > opt.MaxClusterSpread {
		reasons = append(reasons, "phrase too broad for the sample")
	}
	return Verdict{OK: len(reasons) == 0, Reasons: reasons}
}

func allStopwords(tokens []string, stopwords map[string]bool) bool {
	for _, t := range tokens {
		if !stopwords[t] {
			return false
		}
	}
	return true
}
