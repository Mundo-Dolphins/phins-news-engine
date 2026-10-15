// Package classification decides whether an article is related to the
// covered team, from evidence found by a dictionary.Matcher — never a
// single keyword lookup.
package classification

import (
	"sort"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

// Evidence is one dictionary entry's contribution to a Result.
type Evidence struct {
	Category     string
	EntryName    string
	MatchedText  string
	Count        int
	Contribution float64
}

// Result is the outcome of classifying an article's relevance to the covered team.
type Result struct {
	Related           bool
	Score             float64
	PositiveEvidence  []Evidence
	NegativeEvidence  []Evidence
	EntitiesDetected  []string
	RuleApplied       string
	DictionaryVersion string
}

// RuleApplied values. RuleTrustedBypass is never set by Decide itself —
// it's set by ProcessCycle when a trusted source skips scoring entirely
// — but lives here so callers reference one shared constant
// instead of a magic string.
const (
	RuleStrongOverride = "strong_override"
	RulePositiveHigh   = "positive_threshold_high"
	RulePositiveMedium = "positive_threshold_medium"
	RuleNegative       = "negative_threshold"
	RuleBelowThreshold = "below_threshold"
	RuleTrustedBypass  = "trusted_bypass"
	RuleSourceContext  = "source_context"
)

type aggregatedHit struct {
	category    string
	entryName   string
	matchedText string
	rawCount    int
	scoreCount  float64
}

// Decide classifies an article's relevance to the covered team from
// dictionary evidence found in its title and body. Every category with at
// least one match contributes category.Weight * min(occurrences,
// MaxCountPerTerm) to Score, where title occurrences and body occurrences
// are each capped at MaxCountPerTerm independently before the title count
// is multiplied by TitleWeightMultiplier and summed with the body count. A
// category with RequiresAnyOf only contributes if at least one of the
// referenced categories also had a raw match somewhere in the article
// (ambiguity handling: "isotopes" alone doesn't count, "isotopes sign free
// agent" does — this check uses raw matches, not
// post-filter matches, to avoid two categories that mutually require each
// other never being able to activate). A category with StrongOverride that
// contributes at all decides Related=true immediately, regardless of
// Score. Decide never special-cases a source — trusted-source bypass is a
// usecase-layer concern.
func Decide(title, body string, m dictionary.Matcher) Result {
	return decide(title, body, m, false)
}

// DecideWithSourceContext classifies an article from a source whose scope
// establishes that an otherwise ambiguous Isotopes/Topes mention in the
// title means the NFL team. Unlike a trusted-source bypass, this still
// requires positive dictionary evidence and still applies negative
// evidence and score thresholds; a generic or off-topic post does not pass
// merely because it came from a beat reporter or Isotopes aggregator.
func DecideWithSourceContext(title, body string, m dictionary.Matcher) Result {
	return decide(title, body, m, true)
}

func decide(title, body string, m dictionary.Matcher, sourceContext bool) Result {
	decision := m.Decision()
	hits := aggregate(m.Find(title), m.Find(body), decision)

	matched := make(map[string]bool, len(hits))
	for _, h := range hits {
		matched[h.category] = true
	}

	weight := make(map[string]float64)
	strongOverride := make(map[string]bool)
	requiresAnyOf := make(map[string][]string)
	for _, cat := range m.Categories() {
		weight[cat.Name] = cat.Weight
		strongOverride[cat.Name] = cat.StrongOverride
		requiresAnyOf[cat.Name] = cat.RequiresAnyOf
	}

	result := Result{DictionaryVersion: m.Version()}
	entities := map[string]bool{}
	strongOverrideHit := false

	for _, h := range hits {
		requirementsSatisfied := requirementsMet(requiresAnyOf[h.category], matched)
		sourceContextSatisfies := sourceContext && h.category == "ambiguous_team_terms"
		if !requirementsSatisfied && !sourceContextSatisfies {
			continue
		}
		contribution := weight[h.category] * h.scoreCount
		ev := Evidence{Category: h.category, EntryName: h.entryName, MatchedText: h.matchedText, Count: h.rawCount, Contribution: contribution}
		switch {
		case contribution > 0:
			result.PositiveEvidence = append(result.PositiveEvidence, ev)
			entities[h.entryName] = true
		case contribution < 0:
			result.NegativeEvidence = append(result.NegativeEvidence, ev)
		}
		result.Score += contribution
		if strongOverride[h.category] && h.scoreCount > 0 {
			strongOverrideHit = true
		}
	}

	for name := range entities {
		result.EntitiesDetected = append(result.EntitiesDetected, name)
	}
	sort.Strings(result.EntitiesDetected)
	sortEvidence(result.PositiveEvidence)
	sortEvidence(result.NegativeEvidence)

	if strongOverrideHit {
		result.Related = true
		result.RuleApplied = RuleStrongOverride
		return result
	}

	switch {
	case result.Score >= decision.PositiveThresholdHigh:
		result.Related, result.RuleApplied = true, RulePositiveHigh
	case result.Score >= decision.PositiveThresholdMedium:
		result.Related = true
		if sourceContext {
			result.RuleApplied = RuleSourceContext
		} else {
			result.RuleApplied = RulePositiveMedium
		}
	case result.Score < 0 && result.Score <= decision.NegativeThresholdHigh:
		result.Related, result.RuleApplied = false, RuleNegative
	default:
		result.Related, result.RuleApplied = false, RuleBelowThreshold
	}
	return result
}

func requirementsMet(requiresAnyOf []string, matched map[string]bool) bool {
	if len(requiresAnyOf) == 0 {
		return true
	}
	for _, name := range requiresAnyOf {
		if matched[name] {
			return true
		}
	}
	return false
}

// aggregate merges title and body occurrences of the same (category,
// entry) pair into one hit, capping each source's raw count at
// MaxCountPerTerm independently before weighting title occurrences by
// TitleWeightMultiplier — capping after weighting would let the
// multiplier's effect get clipped away by the same cap meant to bound a
// single mention's influence.
func aggregate(titleOcc, bodyOcc []dictionary.Occurrence, d dictionary.Decision) []aggregatedHit {
	type key struct{ category, entry string }
	byKey := map[key]*aggregatedHit{}
	var order []key

	merge := func(occs []dictionary.Occurrence, titleWeighted bool) {
		for _, o := range occs {
			k := key{o.Category, o.EntryName}
			h, ok := byKey[k]
			if !ok {
				h = &aggregatedHit{category: o.Category, entryName: o.EntryName, matchedText: o.MatchedText}
				byKey[k] = h
				order = append(order, k)
			}
			capped := o.Count
			if capped > d.MaxCountPerTerm {
				capped = d.MaxCountPerTerm
			}
			h.rawCount += o.Count
			w := 1.0
			if titleWeighted {
				w = d.TitleWeightMultiplier
			}
			h.scoreCount += float64(capped) * w
		}
	}
	merge(bodyOcc, false)
	merge(titleOcc, true)

	hits := make([]aggregatedHit, 0, len(order))
	for _, k := range order {
		hits = append(hits, *byKey[k])
	}
	return hits
}

func sortEvidence(ev []Evidence) {
	sort.Slice(ev, func(i, j int) bool {
		if ev[i].Category != ev[j].Category {
			return ev[i].Category < ev[j].Category
		}
		return ev[i].EntryName < ev[j].EntryName
	})
}
