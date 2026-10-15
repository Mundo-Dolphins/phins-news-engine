package editorialtrain

import (
	"sort"
	"strings"
)

// PruneConfig bounds BuildSchema's output to a compact, informative
// feature set: MinDocFreq/MaxDocFreq are fractions of training documents
// (a feature present in every document, or in almost none, carries little
// signal); MaxFeatures caps the final count by chi-square score (<=0
// means no cap).
type PruneConfig struct {
	MinDocFreq  float64
	MaxDocFreq  float64
	MaxFeatures int
}

// FeatureSchema is the pruned, ordered, versioned feature list a trained
// Model embeds — built once from the training split only (never
// validation/test, to avoid data leakage).
type FeatureSchema struct {
	Version  string
	Features []string
}

// BuildSchema extracts every candidate feature from train (via
// TrainingExample.Features), keeps only those within
// [MinDocFreq,MaxDocFreq] document frequency, ranks the survivors by
// chi-square association with the publish/reject label, and keeps the top
// MaxFeatures (or all survivors if MaxFeatures <= 0). Ties break by
// featureKindRank, then alphabetically, for determinism (see
// featureKindRank for why alphabetical-only would defeat the "compact,
// informative" goal).
func BuildSchema(train Dataset, cfg PruneConfig, version string) FeatureSchema {
	docFreq := map[string]int{}        // feature -> # docs containing it
	presentPublish := map[string]int{} // feature -> # publish-labeled docs containing it
	presentReject := map[string]int{}  // feature -> # reject-labeled docs containing it
	n := len(train.Examples)

	for _, ex := range train.Examples {
		for feature := range ex.Features() {
			docFreq[feature]++
			if ex.Label == "publish" {
				presentPublish[feature]++
			} else {
				presentReject[feature]++
			}
		}
	}

	totalPublish, totalReject := 0, 0
	for _, ex := range train.Examples {
		if ex.Label == "publish" {
			totalPublish++
		} else {
			totalReject++
		}
	}

	type scored struct {
		feature string
		chi2    float64
	}
	var candidates []scored
	for feature, df := range docFreq {
		freq := float64(df) / float64(n)
		if n == 0 || freq < cfg.MinDocFreq || freq > cfg.MaxDocFreq {
			continue
		}
		a := float64(presentPublish[feature]) // present, publish
		b := float64(presentReject[feature])  // present, reject
		c := float64(totalPublish) - a        // absent, publish
		d := float64(totalReject) - b         // absent, reject
		chi2 := chiSquare(a, b, c, d)
		candidates = append(candidates, scored{feature, chi2})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].chi2 != candidates[j].chi2 {
			return candidates[i].chi2 > candidates[j].chi2
		}
		if ri, rj := featureKindRank(candidates[i].feature), featureKindRank(candidates[j].feature); ri != rj {
			return ri < rj
		}
		return candidates[i].feature < candidates[j].feature
	})

	if cfg.MaxFeatures > 0 && len(candidates) > cfg.MaxFeatures {
		candidates = candidates[:cfg.MaxFeatures]
	}

	features := make([]string, len(candidates))
	for i, c := range candidates {
		features[i] = c.feature
	}
	sort.Strings(features) // final on-disk order is alphabetical, independent of ranking order

	return FeatureSchema{Version: version, Features: features}
}

// SchemaOverlap reports how similar candidate's feature vocabulary is to
// promoted's — Jaccard similarity plus how many features are newly added
// (in candidate, not promoted) or removed (in promoted, not candidate).
// Informational only: Predict already treats a feature absent from a
// model's Weights as contributing 0, so vocabulary drift
// between a candidate and the currently-promoted model can never crash
// or misbehave the runtime regardless of this value — this exists purely
// as an operator signal printed by promote-model, never a gate.
func SchemaOverlap(candidate, promoted FeatureSchema) (jaccard float64, added, removed int) {
	candidateSet := make(map[string]bool, len(candidate.Features))
	for _, f := range candidate.Features {
		candidateSet[f] = true
	}
	promotedSet := make(map[string]bool, len(promoted.Features))
	for _, f := range promoted.Features {
		promotedSet[f] = true
	}

	intersection := 0
	for f := range candidateSet {
		if promotedSet[f] {
			intersection++
		} else {
			added++
		}
	}
	for f := range promotedSet {
		if !candidateSet[f] {
			removed++
		}
	}
	union := len(candidateSet) + len(promotedSet) - intersection
	if union == 0 {
		return 0, added, removed
	}
	return float64(intersection) / float64(union), added, removed
}

// featureKindRank orders a feature's "kind" for BuildSchema's tie-break:
// lower ranks are preferred when two features tie on chi-square.
//
// A character n-gram (char3:/char4:/char5:) drawn from inside a word is
// mechanically perfectly correlated with that word's own unigram feature
// whenever the word itself is perfectly label-correlated: e.g. if
// "isotopes" appears in every publish-labeled document and no
// reject-labeled one, then char3:dol, char3:olp, char3:lph, ... all have
// the exact same 2x2 contingency table, and therefore the exact same
// chi-square score, as uni:isotopes. A word of typical length produces
// well over MaxFeatures such companions. Pure
// alphabetical tie-break ("char3:" < "uni:") would then let a pile of
// redundant, less-interpretable character fragments crowd the far more
// general and informative unigram (or bigram) out of a size-capped schema
// — the opposite of PruneConfig's "compact, informative" goal. Ranking
// unigrams, then bigrams, then other engineered features, then character
// n-grams (in that preference order) before falling back to alphabetical
// order fixes that without changing anything about the chi-square ranking
// itself.
func featureKindRank(feature string) int {
	switch {
	case strings.HasPrefix(feature, "uni:"):
		return 0
	case strings.HasPrefix(feature, "bi:"):
		return 1
	case strings.HasPrefix(feature, "char3:"), strings.HasPrefix(feature, "char4:"), strings.HasPrefix(feature, "char5:"):
		return 3
	default:
		return 2
	}
}

// chiSquare computes the standard 2x2 contingency chi-square statistic for
// (feature present/absent) x (label publish/reject). Returns 0 (no signal)
// if any marginal total is 0, avoiding a division by zero.
func chiSquare(a, b, c, d float64) float64 {
	total := a + b + c + d
	rowPresent, rowAbsent := a+b, c+d
	colPublish, colReject := a+c, b+d
	denominator := rowPresent * rowAbsent * colPublish * colReject
	if denominator == 0 {
		return 0
	}
	numerator := total * (a*d - b*c) * (a*d - b*c)
	return numerator / denominator
}
