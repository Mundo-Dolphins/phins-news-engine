package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// gradedModel scores strictly by how many times "signal" appears, giving a
// spread of values rather than perfectMode's two-point separation, so
// SelectThresholds has a real curve to sweep.
func gradedModel() editorial.Model {
	return editorial.Model{Bias: -3, Weights: map[string]float64{"uni:signal": 3}}
}

func gradedExample(id, label string, signalCount int) editorialtrain.TrainingExample {
	title := ""
	for i := 0; i < signalCount; i++ {
		title += "signal "
	}
	if title == "" {
		title = "neutral"
	}
	return editorialtrain.TrainingExample{ArticleID: id, Title: title, Label: label}
}

func TestSelectThresholds_SeparatesHighAndLowConfidenceRegions(t *testing.T) {
	validate := []editorialtrain.TrainingExample{
		gradedExample("p1", "publish", 5), gradedExample("p2", "publish", 4), gradedExample("p3", "publish", 4),
		gradedExample("r1", "reject", 0), gradedExample("r2", "reject", 0), gradedExample("r3", "reject", 1),
	}
	thresholds := editorialtrain.SelectThresholds(gradedModel(), validate, editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.9, RejectTargetPrecision: 0.9})

	if thresholds.Reject >= thresholds.Accept {
		t.Fatalf("Reject (%v) >= Accept (%v), want Reject < Accept for a separable validation set", thresholds.Reject, thresholds.Accept)
	}

	// Every example at or above Accept must score there because it's
	// genuinely publish-heavy; every example at or below Reject must be
	// reject-heavy. Spot-check the clearest members of each group.
	p1Score := editorial.Predict(gradedModel(), (&editorialtrain.TrainingExample{ArticleID: "p1", Title: "signal signal signal signal signal", Label: "publish"}).Features())
	if p1Score.Value < thresholds.Accept {
		t.Errorf("strongest publish example scored %v, want >= Accept threshold %v", p1Score.Value, thresholds.Accept)
	}
}

func TestSelectThresholds_NoSeparationFallsBackToFullyAmbiguous(t *testing.T) {
	// Every example scores identically (no "signal" mentions at all,
	// regardless of label) — there's no threshold that can hit 90%
	// precision on either side, so SelectThresholds must not fabricate one.
	validate := []editorialtrain.TrainingExample{
		gradedExample("p1", "publish", 0), gradedExample("r1", "reject", 0),
	}
	thresholds := editorialtrain.SelectThresholds(gradedModel(), validate, editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.9, RejectTargetPrecision: 0.9})
	if thresholds.Reject != 0 || thresholds.Accept != 100 {
		t.Errorf("Thresholds = %+v, want the fully-ambiguous fallback {Reject:0, Accept:100} when no cut point meets the target precision", thresholds)
	}
}

func TestSelectThresholds_EmptyValidationSetReturnsFallback(t *testing.T) {
	thresholds := editorialtrain.SelectThresholds(gradedModel(), nil, editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.9, RejectTargetPrecision: 0.9})
	if thresholds.Reject != 0 || thresholds.Accept != 100 {
		t.Errorf("Thresholds = %+v, want the fully-ambiguous fallback for an empty validation set", thresholds)
	}
}

func TestSelectThresholds_AsymmetricTargetsProduceDifferentBoundaries(t *testing.T) {
	// Three score tiers, not two: a pure-reject floor (signal count 0), a
	// mixed middle tier (signal count 1, one reject + one publish — 80%
	// suffix precision from there up), and a pure-publish ceiling (signal
	// counts 3-5). With the brief's original two-tier fixture, loosening
	// the accept target let the accept boundary slide all the way down to
	// meet the reject boundary at the same score, tripping SelectThresholds'
	// own reject>=accept fallback guard and masking the asymmetry the test
	// means to observe. The middle tier gives the accept boundary a rung to
	// land on (0.8 precision, between 0.6 and 0.9) that's still clearly
	// above the reject boundary.
	validate := []editorialtrain.TrainingExample{
		gradedExample("r1", "reject", 0), gradedExample("r2", "reject", 0), gradedExample("r3", "reject", 0), gradedExample("r4", "reject", 0),
		gradedExample("r5", "reject", 1), gradedExample("p1", "publish", 1),
		gradedExample("p2", "publish", 3), gradedExample("p3", "publish", 4), gradedExample("p4", "publish", 5),
	}
	symmetric := editorialtrain.SelectThresholds(gradedModel(), validate, editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.9, RejectTargetPrecision: 0.9})
	looseAccept := editorialtrain.SelectThresholds(gradedModel(), validate, editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.6, RejectTargetPrecision: 0.9})

	if looseAccept.Accept >= symmetric.Accept {
		t.Errorf("looseAccept.Accept = %v, want < symmetric.Accept (%v) — a lower accept-side target should land on a genuinely lower score bar", looseAccept.Accept, symmetric.Accept)
	}
	if looseAccept.Reject != symmetric.Reject {
		t.Errorf("looseAccept.Reject = %v, want == symmetric.Reject (%v) — only the accept-side target changed", looseAccept.Reject, symmetric.Reject)
	}
}

// fprValidate mirrors the real corpus shape SelectThresholds' precision
// sweep could not handle: publish dominates (base rate > the accept
// precision target), so precision alone never separates anything.
func fprValidate() []editorialtrain.TrainingExample {
	return []editorialtrain.TrainingExample{
		gradedExample("r1", "reject", 0), gradedExample("r2", "reject", 0), gradedExample("r3", "reject", 1),
		gradedExample("r4", "reject", 3), gradedExample("p0", "publish", 1),
		gradedExample("p1", "publish", 3), gradedExample("p2", "publish", 4), gradedExample("p3", "publish", 4),
		gradedExample("p4", "publish", 5), gradedExample("p5", "publish", 5), gradedExample("p6", "publish", 5),
		gradedExample("p7", "publish", 5), gradedExample("p8", "publish", 5), gradedExample("p9", "publish", 5),
		gradedExample("pa", "publish", 5), gradedExample("pb", "publish", 5), gradedExample("pc", "publish", 5),
		gradedExample("pd", "publish", 5), gradedExample("pe", "publish", 5), gradedExample("pf", "publish", 5),
	}
}

func scoreOf(signalCount int) float64 {
	return editorial.Predict(gradedModel(), gradedExample("x", "publish", signalCount).Features()).Value
}

func TestSelectAcceptThresholdByFPR_PicksLowestAcceptWithinCap(t *testing.T) {
	// 4 rejects: accepting signal>=3 lets r4 through (FPR 0.25); accepting
	// signal>=1 would also let r3 through (FPR 0.5).
	thresholds := editorialtrain.SelectAcceptThresholdByFPR(gradedModel(), fprValidate(), 0.25)
	if thresholds.Accept != scoreOf(3) {
		t.Errorf("Accept = %v, want %v (score at signal=3, the lowest cut with FPR <= 0.25)", thresholds.Accept, scoreOf(3))
	}
	if thresholds.Reject >= thresholds.Accept {
		t.Errorf("Reject = %v, want below Accept %v", thresholds.Reject, thresholds.Accept)
	}
}

func TestSelectAcceptThresholdByFPR_TighterCapRaisesAccept(t *testing.T) {
	thresholds := editorialtrain.SelectAcceptThresholdByFPR(gradedModel(), fprValidate(), 0.1)
	if thresholds.Accept != scoreOf(4) {
		t.Errorf("Accept = %v, want %v (signal=4: no reject accepted)", thresholds.Accept, scoreOf(4))
	}
}

func TestSelectAcceptThresholdByFPR_NoRejectsFallsBackToFullyAmbiguous(t *testing.T) {
	// Without negatives FPR is unmeasurable, so no automatic accept is safe.
	validate := []editorialtrain.TrainingExample{gradedExample("p1", "publish", 5), gradedExample("p2", "publish", 1)}
	thresholds := editorialtrain.SelectAcceptThresholdByFPR(gradedModel(), validate, 0.15)
	if thresholds.Reject != 0 || thresholds.Accept != 100 {
		t.Errorf("Thresholds = %+v, want fully-ambiguous fallback", thresholds)
	}
}

func TestSelectAcceptThresholdByFPR_TopTieGroupOverCapFallsBack(t *testing.T) {
	validate := []editorialtrain.TrainingExample{
		gradedExample("p1", "publish", 5), gradedExample("r1", "reject", 5), gradedExample("r2", "reject", 0),
	}
	thresholds := editorialtrain.SelectAcceptThresholdByFPR(gradedModel(), validate, 0.15)
	if thresholds.Reject != 0 || thresholds.Accept != 100 {
		t.Errorf("Thresholds = %+v, want fully-ambiguous fallback when even the top score tie group exceeds the cap", thresholds)
	}
}
