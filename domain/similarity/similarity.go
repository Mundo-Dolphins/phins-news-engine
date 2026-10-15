// Package similarity computes explainable overlap scores between two sets
// of string shingles. It deliberately does not use SimHash or MinHash —
// see ADR 0009 for why, at this bot's realistic candidate-set size, exact
// Jaccard/Dice over an already SQL-filtered candidate list is both cheaper
// to reason about and more explainable than an approximate sketch.
package similarity

// Jaccard returns |a ∩ b| / |a ∪ b| treating a and b as sets (duplicate
// elements within a slice do not increase their weight). Returns 0 when
// both are empty.
func Jaccard(a, b []string) float64 {
	sa, sb := toSet(a), toSet(b)
	inter := intersectionSize(sa, sb)
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// Dice returns 2*|a ∩ b| / (|a|+|b|) treating a and b as sets. Returns 0
// when both are empty.
func Dice(a, b []string) float64 {
	sa, sb := toSet(a), toSet(b)
	inter := intersectionSize(sa, sb)
	denom := len(sa) + len(sb)
	if denom == 0 {
		return 0
	}
	return 2 * float64(inter) / float64(denom)
}

func toSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func intersectionSize(a, b map[string]bool) int {
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	count := 0
	for item := range small {
		if large[item] {
			count++
		}
	}
	return count
}

// Containment returns |a ∩ b| / |a| over the two inputs treated as sets: how
// much of a is covered by b. Used when a is a truncated copy of b (social
// posts that cut a headline with an ellipsis), where Jaccard would punish
// the missing tail.
func Containment(a, b []string) float64 {
	setA := make(map[string]struct{}, len(a))
	for _, x := range a {
		setA[x] = struct{}{}
	}
	if len(setA) == 0 {
		return 0
	}
	setB := make(map[string]struct{}, len(b))
	for _, x := range b {
		setB[x] = struct{}{}
	}
	shared := 0
	for x := range setA {
		if _, ok := setB[x]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(setA))
}
