package editorialtrain

// SourceConcentration returns the SourceID contributing the most examples
// and its share of len(examples), so an operator can see when one
// publisher dominates a dataset and the classifier risks learning that
// publisher's writing style instead of relevance.
// Ties resolve to the alphabetically first SourceID for stable output; an
// empty dataset returns ("", 0).
func SourceConcentration(examples []TrainingExample) (string, float64) {
	if len(examples) == 0 {
		return "", 0
	}
	counts := make(map[string]int)
	for _, ex := range examples {
		counts[ex.SourceID]++
	}
	top, topCount := "", 0
	for source, n := range counts {
		if n > topCount || (n == topCount && source < top) {
			top, topCount = source, n
		}
	}
	return top, float64(topCount) / float64(len(examples))
}
