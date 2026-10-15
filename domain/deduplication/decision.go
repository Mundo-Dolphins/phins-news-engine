package deduplication

import "fmt"

// DuplicateType classifies why (or whether) Decide found a match.
type DuplicateType string

const (
	DuplicateTypeExact     DuplicateType = "exact"
	DuplicateTypeNear      DuplicateType = "near"
	DuplicateTypeSameEvent DuplicateType = "same_event"
	DuplicateTypeNone      DuplicateType = "none"
)

// Candidate is the minimal shape Decide needs from a near-duplicate
// candidate: which article it is and which cluster it currently belongs
// to. The richer application-layer Candidate (title, source, timestamps)
// lives in ports/out — this package stays free of that dependency.
type Candidate struct {
	ArticleID string
	ClusterID string
}

// ThresholdConfig is the raw, source-type-aware threshold configuration an
// application layer resolves once per article via ResolveThresholds.
type ThresholdConfig struct {
	DefaultThreshold      float64
	ThresholdBySourceType map[string]float64
	ShortTextMaxTokens    int
	ShortTextThreshold    float64
	EventThreshold        float64
	RulesVersion          string
}

// Thresholds is the resolved pair of bands Decide compares a candidate's
// score against.
type Thresholds struct {
	DuplicateThreshold float64
	EventThreshold     float64
	RulesVersion       string
}

// ResolveThresholds picks the duplicate threshold for one comparison: the
// per-source-type override if configured, then the short-text override if
// the current article's token count is at or below ShortTextMaxTokens
// (short text needs a stricter/looser bar than prose headlines).
func ResolveThresholds(cfg ThresholdConfig, sourceType string, tokenCount int) Thresholds {
	duplicateThreshold := cfg.DefaultThreshold
	if t, ok := cfg.ThresholdBySourceType[sourceType]; ok {
		duplicateThreshold = t
	}
	if tokenCount <= cfg.ShortTextMaxTokens {
		duplicateThreshold = cfg.ShortTextThreshold
	}
	return Thresholds{
		DuplicateThreshold: duplicateThreshold,
		EventThreshold:     cfg.EventThreshold,
		RulesVersion:       cfg.RulesVersion,
	}
}

// Decision is the outcome of comparing an article against its best-scoring
// candidate.
type Decision struct {
	IsDuplicate      bool
	Type             DuplicateType
	MatchedArticleID string
	MatchedClusterID string
	Score            float64
	Threshold        float64
	Signals          []string
	RulesVersion     string
	Explanation      string
}

// Decide finds the highest-scoring candidate in scores and classifies it
// into one of three bands: at or above t.DuplicateThreshold is a near
// duplicate (IsDuplicate=true, rejected the same as an exact duplicate); at
// or above t.EventThreshold but below that is the same event (not
// rejected — clustering.DecideJoin reuses this same match); otherwise no
// match. candidates and scores are produced once by the caller from a
// single similarity computation and reused for both this decision and the
// clustering decision — Decide never recomputes a score itself.
func Decide(candidates []Candidate, scores map[string]float64, signals map[string][]string, t Thresholds) Decision {
	best, bestScore, found := bestCandidate(candidates, scores)
	if !found {
		return Decision{Type: DuplicateTypeNone, Threshold: t.EventThreshold, RulesVersion: t.RulesVersion, Explanation: "no candidates within the search window"}
	}

	switch {
	case bestScore >= t.DuplicateThreshold:
		return Decision{
			IsDuplicate: true, Type: DuplicateTypeNear,
			MatchedArticleID: best.ArticleID, MatchedClusterID: best.ClusterID,
			Score: bestScore, Threshold: t.DuplicateThreshold,
			Signals: signals[best.ArticleID], RulesVersion: t.RulesVersion,
			Explanation: fmt.Sprintf("near-duplicate of %s: score %.2f >= threshold %.2f (rules %s)", best.ArticleID, bestScore, t.DuplicateThreshold, t.RulesVersion),
		}
	case bestScore >= t.EventThreshold:
		return Decision{
			Type:             DuplicateTypeSameEvent,
			MatchedArticleID: best.ArticleID, MatchedClusterID: best.ClusterID,
			Score: bestScore, Threshold: t.EventThreshold,
			Signals: signals[best.ArticleID], RulesVersion: t.RulesVersion,
			Explanation: fmt.Sprintf("same event as %s: score %.2f in [%.2f, %.2f) (rules %s)", best.ArticleID, bestScore, t.EventThreshold, t.DuplicateThreshold, t.RulesVersion),
		}
	default:
		return Decision{
			Type: DuplicateTypeNone, Score: bestScore, Threshold: t.EventThreshold, RulesVersion: t.RulesVersion,
			Explanation: fmt.Sprintf("best candidate %s scored %.2f, below event threshold %.2f", best.ArticleID, bestScore, t.EventThreshold),
		}
	}
}

func bestCandidate(candidates []Candidate, scores map[string]float64) (Candidate, float64, bool) {
	var best Candidate
	bestScore := -1.0
	found := false
	for _, c := range candidates {
		s, ok := scores[c.ArticleID]
		if !ok || s < bestScore {
			continue
		}
		if !found || s > bestScore {
			bestScore = s
			best = c
			found = true
		}
	}
	return best, bestScore, found
}
