// domain/reviewpriority/reviewpriority_test.go
package reviewpriority_test

import (
	"math"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
)

func testConfig() reviewpriority.Config {
	return reviewpriority.Config{
		Weights: reviewpriority.Weights{
			Disagreement: 1.0, NBUncertainty: 0.7, LLMUncertainty: 0.7,
			NovelVocabulary: 0.5, UnknownEntities: 0.5, NewSource: 0.3, Recency: 0.2,
		},
		LLMConfidenceAutoLabelThreshold: 0.9,
		LLMConfidenceUncertainThreshold: 0.5,
		NewSourceWindowHours:            72,
		RecencyWindowHours:              24,
	}
}

func TestScore_ObviousAgreementIsSkipped(t *testing.T) {
	in := reviewpriority.Input{
		NBZone: editorial.ZoneAccept, NBValue: 95, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		LLMLabel: "publish", LLMConfidence: 0.95,
		ArticleAgeHours: 1000, // even a very stale article must be skipped
		Signals:         reviewpriority.Signals{UnknownEntityCount: 10, PendingDictionaryProposalCount: 10},
		Now:             time.Now(),
	}
	got := reviewpriority.Score(in, testConfig())
	if !got.Skip {
		t.Fatalf("Score().Skip = false, want true for a confident NB/LLM agreement")
	}
}

func TestScore_AmbiguousNBNeverCountsAsObviousAgreement(t *testing.T) {
	in := reviewpriority.Input{
		NBZone: editorial.ZoneAmbiguous, NBValue: 50, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		LLMLabel: "publish", LLMConfidence: 0.99,
		Now: time.Now(),
	}
	got := reviewpriority.Score(in, testConfig())
	if got.Skip {
		t.Fatalf("Score().Skip = true, want false when NB is ambiguous")
	}
}

func TestScore_LowConfidenceLLMNeverCountsAsObviousAgreement(t *testing.T) {
	in := reviewpriority.Input{
		NBZone: editorial.ZoneAccept, NBValue: 95, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		LLMLabel: "publish", LLMConfidence: 0.6, // below LLMConfidenceAutoLabelThreshold
		Now: time.Now(),
	}
	got := reviewpriority.Score(in, testConfig())
	if got.Skip {
		t.Fatalf("Score().Skip = true, want false when LLM confidence is below the auto-label threshold")
	}
}

func TestScore_DisagreementDominatesScore(t *testing.T) {
	agree := reviewpriority.Input{
		NBZone: editorial.ZoneAccept, NBValue: 95, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		LLMLabel: "publish", LLMConfidence: 0.6, Now: time.Now(),
	}
	disagree := agree
	disagree.LLMLabel = "discard"

	agreeScore := reviewpriority.Score(agree, testConfig())
	disagreeScore := reviewpriority.Score(disagree, testConfig())
	if disagreeScore.Skip || agreeScore.Skip {
		t.Fatalf("neither case should be Skip (LLM confidence 0.6 < auto-label threshold 0.9)")
	}
	if disagreeScore.Score <= agreeScore.Score {
		t.Errorf("disagreement score %v should be > agreement score %v", disagreeScore.Score, agreeScore.Score)
	}
}

func TestScore_NBUncertaintyPeaksAtThresholdMidpoint(t *testing.T) {
	thresholds := editorial.Thresholds{Reject: 20, Accept: 80}
	mid := reviewpriority.Input{NBZone: editorial.ZoneAmbiguous, NBValue: 50, NBThresholds: thresholds, LLMLabel: "uncertain", LLMConfidence: 0.5, Now: time.Now()}
	edge := reviewpriority.Input{NBZone: editorial.ZoneAmbiguous, NBValue: 79, NBThresholds: thresholds, LLMLabel: "uncertain", LLMConfidence: 0.5, Now: time.Now()}

	cfg := testConfig()
	midScore := reviewpriority.Score(mid, cfg)
	edgeScore := reviewpriority.Score(edge, cfg)
	if midScore.Score <= edgeScore.Score {
		t.Errorf("midpoint score %v should be > near-threshold-edge score %v", midScore.Score, edgeScore.Score)
	}
}

func TestScore_LLMUncertaintyDecreasesWithConfidence(t *testing.T) {
	cfg := testConfig()
	low := reviewpriority.Input{NBZone: editorial.ZoneAmbiguous, NBValue: 50, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80}, LLMLabel: "uncertain", LLMConfidence: 0.5, Now: time.Now()}
	high := low
	high.LLMConfidence = 0.85

	lowScore := reviewpriority.Score(low, cfg)
	highScore := reviewpriority.Score(high, cfg)
	if highScore.Score >= lowScore.Score {
		t.Errorf("higher LLM confidence score %v should be < lower confidence score %v (both non-skip, everything else equal)", highScore.Score, lowScore.Score)
	}
}

func TestScore_NovelVocabularyAndUnknownEntitiesIncreaseScore(t *testing.T) {
	cfg := testConfig()
	base := reviewpriority.Input{NBZone: editorial.ZoneAmbiguous, NBValue: 50, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80}, LLMLabel: "uncertain", LLMConfidence: 0.5, Now: time.Now()}
	withSignals := base
	withSignals.Signals = reviewpriority.Signals{UnknownEntityCount: 5, PendingDictionaryProposalCount: 3}

	baseScore := reviewpriority.Score(base, cfg)
	signalScore := reviewpriority.Score(withSignals, cfg)
	if signalScore.Score <= baseScore.Score {
		t.Errorf("score with novel-vocabulary/unknown-entity signals %v should be > base score %v", signalScore.Score, baseScore.Score)
	}
}

func TestScore_NewSourceAndRecencyDecayToZero(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	fresh := reviewpriority.Input{
		NBZone: editorial.ZoneAmbiguous, NBValue: 50, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		LLMLabel: "uncertain", LLMConfidence: 0.5, ArticleAgeHours: 0,
		Signals: reviewpriority.Signals{SourceFirstSeenKnown: true, SourceFirstSeen: now},
		Now:     now,
	}
	stale := fresh
	stale.ArticleAgeHours = 1000
	stale.Signals.SourceFirstSeen = now.Add(-1000 * time.Hour)

	freshScore := reviewpriority.Score(fresh, cfg)
	staleScore := reviewpriority.Score(stale, cfg)
	if staleScore.Score >= freshScore.Score {
		t.Errorf("stale/old-source score %v should be < fresh score %v", staleScore.Score, freshScore.Score)
	}
}

func TestScore_ReasonNamesTopContributors(t *testing.T) {
	in := reviewpriority.Input{
		NBZone: editorial.ZoneAccept, NBValue: 95, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		LLMLabel: "discard", LLMConfidence: 0.95, Now: time.Now(),
	}
	got := reviewpriority.Score(in, testConfig())
	if got.Skip {
		t.Fatalf("Score().Skip = true, want false (NB/LLM disagree)")
	}
	if got.Reason == "" || got.Reason == "no significant signal" {
		t.Errorf("Reason = %q, want it to name the disagreement", got.Reason)
	}
}

func TestScore_ScoreIsFinite(t *testing.T) {
	in := reviewpriority.Input{NBZone: editorial.ZoneAmbiguous, NBValue: 50, NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80}, LLMLabel: "uncertain", LLMConfidence: 0.5, Now: time.Now()}
	got := reviewpriority.Score(in, testConfig())
	if math.IsNaN(got.Score) || math.IsInf(got.Score, 0) {
		t.Errorf("Score = %v, want a finite number", got.Score)
	}
}

func TestScore_ZeroValueNBZoneNeverCountsAsObviousAgreement(t *testing.T) {
	in := reviewpriority.Input{
		// NBZone deliberately left at its Go zero value ("") -- must
		// never be treated as a confident NB verdict.
		LLMLabel: "uncertain", LLMConfidence: 0.99,
		NBThresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		Now:          time.Now(),
	}
	got := reviewpriority.Score(in, testConfig())
	if got.Skip {
		t.Fatalf("Score().Skip = true, want false — a zero-value NBZone must never be an obvious agreement")
	}
}

func TestScore_HardMiningZeroWeightIsNoOp(t *testing.T) {
	cfg := reviewpriority.Config{Weights: reviewpriority.Weights{Disagreement: 1.0, HardMining: 0}}
	base := reviewpriority.Input{
		NBZone: editorial.ZoneReject, NBValue: 40, NBThresholds: editorial.Thresholds{Reject: 30, Accept: 70},
		LLMLabel: "publish", LLMConfidence: 0.9, Now: time.Now(),
	}
	without := base
	without.Signals.HardMiningCount = 0
	with := base
	with.Signals.HardMiningCount = 1

	gotWithout := reviewpriority.Score(without, cfg)
	gotWith := reviewpriority.Score(with, cfg)
	if gotWithout.Score != gotWith.Score {
		t.Errorf("Score with HardMiningCount=1 = %v, want equal to HardMiningCount=0 (%v) when Weights.HardMining is 0", gotWith.Score, gotWithout.Score)
	}
}

func TestScore_HardMiningPositiveWeightBoosts(t *testing.T) {
	cfg := reviewpriority.Config{Weights: reviewpriority.Weights{Disagreement: 1.0, HardMining: 0.5}}
	base := reviewpriority.Input{
		NBZone: editorial.ZoneReject, NBValue: 40, NBThresholds: editorial.Thresholds{Reject: 30, Accept: 70},
		LLMLabel: "publish", LLMConfidence: 0.9, Now: time.Now(),
	}
	without := base
	without.Signals.HardMiningCount = 0
	with := base
	with.Signals.HardMiningCount = 1

	gotWithout := reviewpriority.Score(without, cfg)
	gotWith := reviewpriority.Score(with, cfg)
	if gotWith.Score <= gotWithout.Score {
		t.Errorf("Score with HardMiningCount=1 = %v, want strictly greater than HardMiningCount=0 (%v) when Weights.HardMining > 0", gotWith.Score, gotWithout.Score)
	}
	if gotWith.Score-gotWithout.Score != 0.5 {
		t.Errorf("Score delta = %v, want exactly 0.5 (1.0 signal * 0.5 weight)", gotWith.Score-gotWithout.Score)
	}
}
