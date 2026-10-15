package trainrun

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// result builds a modeltraining.Result whose test-split confusion matrix
// yields the precision/recall the caller wants to rank candidates by.
func result(algorithm string, tp, fp, fn, tn int, gatesPassed bool) modeltraining.Result {
	return modeltraining.Result{
		Version: algorithm + "-candidate-1",
		Dir:     "/tmp/" + algorithm,
		Candidate: editorialtrain.Candidate{
			Model:      editorial.Model{Algorithm: algorithm},
			TestReport: editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: tp, FP: fp, FN: fn, TN: tn}},
		},
		Gates: editorialtrain.GateResult{Passed: gatesPassed},
	}
}

// TestBestPassingCandidate_NeverSelectsTrivialMajority pins the safety
// rule modeltraining.SelectWinner already enforces: trivial_majority is
// a comparison-only floor (editorialtrain.TrivialTrainer), and `train
// run` promotes without a human in the loop, so a degenerate
// majority-class predictor must never be promotable — not even when it
// scores the highest test F1 of every gate-passing candidate, which it
// readily can on an imbalanced dataset (recall 1.0, precision = base
// rate).
func TestBestPassingCandidate_NeverSelectsTrivialMajority(t *testing.T) {
	// trivial_majority: predicts publish for everything -> recall 1.0,
	// precision 0.9, F1 ~0.947. naive_bayes: F1 ~0.889. The trivial one
	// wins on raw F1 and passes its gates, and must still lose.
	results := []modeltraining.Result{
		result("naive_bayes", 8, 0, 2, 10, true),
		result("trivial_majority", 18, 2, 0, 0, true),
	}

	best, found := bestPassingCandidate(results)

	if !found {
		t.Fatal("bestPassingCandidate() found = false, want the naive_bayes candidate")
	}
	if best.Candidate.Model.Algorithm != "naive_bayes" {
		t.Errorf("bestPassingCandidate() algorithm = %q, want %q (trivial_majority is never eligible)",
			best.Candidate.Model.Algorithm, "naive_bayes")
	}
}

// TestBestPassingCandidate_TrivialMajorityAloneIsNoCandidate covers the
// case where excluding trivial_majority leaves nothing: Run must then
// take its REJECT path rather than promoting the floor.
func TestBestPassingCandidate_TrivialMajorityAloneIsNoCandidate(t *testing.T) {
	results := []modeltraining.Result{
		result("naive_bayes", 8, 0, 2, 10, false),
		result("trivial_majority", 18, 2, 0, 0, true),
	}

	best, found := bestPassingCandidate(results)

	if found {
		t.Errorf("bestPassingCandidate() = %+v, found = true, want no eligible candidate", best)
	}
}

func TestBestPassingCandidate_OnlyNaiveBayesIsEligible(t *testing.T) {
	results := []modeltraining.Result{
		result("naive_bayes", 6, 2, 4, 8, true),         // F1 0.667
		result("logistic_regression", 9, 1, 1, 9, true), // F1 0.900
		result("linear_svm", 10, 0, 0, 10, false),       // F1 1.000 but gates failed
	}

	best, found := bestPassingCandidate(results)

	if !found {
		t.Fatal("bestPassingCandidate() found = false, want the naive_bayes candidate")
	}
	if best.Candidate.Model.Algorithm != "naive_bayes" {
		t.Errorf("bestPassingCandidate() algorithm = %q, want %q", best.Candidate.Model.Algorithm, "naive_bayes")
	}
}
