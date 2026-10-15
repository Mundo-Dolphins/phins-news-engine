package editorialtrain

import (
	"sort"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// ThresholdConfig sets independent target precisions for the accept and
// reject sides of SelectThresholds' sweep — split out from a single
// shared value so asymmetric costs (see CostConfig) can be reflected:
// a higher AcceptTargetPrecision demands more confidence before
// auto-publishing; a higher RejectTargetPrecision demands more
// confidence before auto-rejecting. See the design doc §4 for the
// documented calibration procedure that picks values for both.
type ThresholdConfig struct {
	AcceptTargetPrecision float64
	RejectTargetPrecision float64
	// MaxFalsePositiveRate, when > 0 and the ambiguous policy is
	// "reject", switches Train to SelectAcceptThresholdByFPR and ignores
	// both precision targets. <= 0 keeps the two-sided precision sweep.
	MaxFalsePositiveRate float64
}

// SelectThresholds sweeps validate (never train or test, to avoid leakage) to find the tightest Accept/Reject boundaries that each
// reach their respective side's target precision in cfg, defaulting to the
// fully ambiguous band {Reject: 0, Accept: 100} — every article routed to
// the ambiguous zone rather than an automatic decision — whenever no
// boundary clears the target, or when a degenerate boundary pair would
// cross (Reject >= Accept). This is a deliberately conservative failure
// mode: prioritizing precision without destroying recall (the prompt's own
// framing) means "make no automatic call" beats "guess."
//
// Candidate boundaries are only evaluated at the first (Accept side) or
// last (Reject side) validate index carrying a given score. Predict's own
// zone check is a value comparison (value >= Accept, value <= Reject), so
// every validate example that ties the chosen boundary value ends up on
// the accepted/rejected side once the threshold is deployed — the swept
// precision has to be computed over that entire tie group, not a slice
// that stops partway through it, or it can overstate the precision the
// deployed boundary will actually deliver.
func SelectThresholds(m editorial.Model, validate []TrainingExample, cfg ThresholdConfig) editorial.Thresholds {
	fallback := editorial.Thresholds{Reject: 0, Accept: 100}
	if len(validate) == 0 {
		return fallback
	}

	type sv struct {
		value   float64
		publish bool
	}
	scored := make([]sv, len(validate))
	for i, ex := range validate {
		s := editorial.Predict(m, ex.Features())
		scored[i] = sv{s.Value, ex.Label == "publish"}
	}

	sort.Slice(scored, func(i, j int) bool { return scored[i].value < scored[j].value })
	n := len(scored)

	accept := 100.0
	acceptFound := false
	for i := 0; i < n; i++ {
		if i > 0 && scored[i].value == scored[i-1].value {
			// Not the first index at this score: the suffix starting here
			// would omit earlier members of the same tie group that
			// Predict would still classify as accepted at this threshold.
			continue
		}
		tp, total := 0, 0
		for j := i; j < n; j++ {
			total++
			if scored[j].publish {
				tp++
			}
		}
		if float64(tp)/float64(total) >= cfg.AcceptTargetPrecision {
			accept = scored[i].value
			acceptFound = true
			break
		}
	}

	reject := 0.0
	rejectFound := false
	for i := n - 1; i >= 0; i-- {
		if i < n-1 && scored[i].value == scored[i+1].value {
			// Not the last index at this score: the prefix ending here
			// would omit later members of the same tie group that
			// Predict would still classify as rejected at this threshold.
			continue
		}
		tn, total := 0, 0
		for j := 0; j <= i; j++ {
			total++
			if !scored[j].publish {
				tn++
			}
		}
		if float64(tn)/float64(total) >= cfg.RejectTargetPrecision {
			reject = scored[i].value
			rejectFound = true
			break
		}
	}

	if !acceptFound || !rejectFound || reject >= accept {
		return fallback
	}
	return editorial.Thresholds{Reject: reject, Accept: accept}
}

// SelectAcceptThresholdByFPR picks the lowest Accept boundary whose
// validate false-positive rate (rejects accepted / all rejects) stays at
// or below maxFPR — the quantity EvaluateGates actually checks. It is the
// threshold rule for ambiguous_policy=reject, where the ambiguous zone is
// already a rejection and a Reject boundary adds nothing: SelectThresholds'
// two-sided precision sweep cannot serve that policy on a publish-heavy
// corpus, because an accept-side precision target below the publish base
// rate accepts everything, and a reject-side target the minority class
// can never reach collapses both boundaries to the fully ambiguous
// fallback (every article rejected). Falls back to {Reject: 0, Accept: 100}
// when validate has no reject examples (FPR is unmeasurable) or when even
// the highest-scoring tie group exceeds maxFPR. Tie groups are evaluated
// whole, as in SelectThresholds, since Predict accepts every example at
// the boundary value.
func SelectAcceptThresholdByFPR(m editorial.Model, validate []TrainingExample, maxFPR float64) editorial.Thresholds {
	fallback := editorial.Thresholds{Reject: 0, Accept: 100}

	type sv struct {
		value   float64
		publish bool
	}
	scored := make([]sv, len(validate))
	negatives := 0
	for i, ex := range validate {
		scored[i] = sv{editorial.Predict(m, ex.Features()).Value, ex.Label == "publish"}
		if !scored[i].publish {
			negatives++
		}
	}
	if negatives == 0 {
		return fallback
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].value > scored[j].value })

	accept := 0.0
	found := false
	fp := 0
	for i := 0; i < len(scored); {
		j := i
		for j < len(scored) && scored[j].value == scored[i].value {
			if !scored[j].publish {
				fp++
			}
			j++
		}
		if float64(fp)/float64(negatives) > maxFPR {
			break
		}
		accept, found = scored[i].value, true
		i = j
	}
	if !found {
		return fallback
	}
	return editorial.Thresholds{Reject: 0, Accept: accept}
}
