package editorialtrain

// CostConfig assigns an explicit, asymmetric cost to each of a binary
// classifier's two error types — a false publish (FP: something bad
// reached readers) and a missed team story (FN: something real was
// silently dropped) are not judged equally costly. See the design doc §4.
type CostConfig struct {
	FalsePublishCost float64
	MissedNewsCost   float64
}

// ExpectedCost is the total cost-weighted error count for r:
// FP*costs.FalsePublishCost + FN*costs.MissedNewsCost. A method (not a
// stored Report field) so Report itself stays cost-policy-agnostic —
// costs are a separate operator choice applied on demand to the counts
// Report already has.
func (r Report) ExpectedCost(costs CostConfig) float64 {
	return float64(r.Overall.FP)*costs.FalsePublishCost + float64(r.Overall.FN)*costs.MissedNewsCost
}
