// Package retraintrigger evaluates whether accumulated training/review
// signals justify kicking off an unattended `train run --auto` (issue
// #253 part 23). Evaluate is a pure function, no I/O — same style as
// domain/reviewpriority.Score. Callers gather Signals
// themselves; see application/trainrun.GatherAutoSignals.
package retraintrigger

import "fmt"

// Config is the six independently-optional trigger thresholds —
// mirrors config.RetrainTriggersConfig one-to-one, minus AutoEnabled
// (checked separately by the CLI before Evaluate is ever called; see
// the design spec). The cmd layer converts between the two (see
// PromotionCriteria's doc comment in application/modelpromotion for
// why this package cannot import infrastructure/config).
type Config struct {
	MinNewGoldExamples       int
	MinNewSilverExamples     int
	MaxDisagreementRate      float64
	MaxManualErrorRate       float64
	NovelVocabularyThreshold int
	MinHardExampleCount      int
}

// Signals is the current state Evaluate checks cfg's thresholds
// against — see application/trainrun.GatherAutoSignals for how these
// are gathered from real training/review data.
type Signals struct {
	NewGoldSinceLastRun   int
	NewSilverSinceLastRun int
	DisagreementRate      float64
	ManualErrorRate       float64
	NovelVocabularyCount  int
	HardExampleCount      int
}

// Result is Evaluate's output: whether any threshold fired, and one
// human-readable reason per threshold that did (in Config field order),
// suitable for trainreport.Input.TriggerReasons.
type Result struct {
	Triggered bool
	Reasons   []string
}

// Evaluate checks every non-zero threshold in cfg against signals.
// Each threshold is independent: a zero threshold never fires, and a
// signal exactly at a MinX threshold fires (>=), while a signal exactly
// at a MaxX threshold does not (only a strict > fires) — MinX describes
// "at least this many", MaxX describes "must not exceed this".
func Evaluate(cfg Config, signals Signals) Result {
	var reasons []string
	if cfg.MinNewGoldExamples > 0 && signals.NewGoldSinceLastRun >= cfg.MinNewGoldExamples {
		reasons = append(reasons, fmt.Sprintf("%d new Gold examples (threshold %d)", signals.NewGoldSinceLastRun, cfg.MinNewGoldExamples))
	}
	if cfg.MinNewSilverExamples > 0 && signals.NewSilverSinceLastRun >= cfg.MinNewSilverExamples {
		reasons = append(reasons, fmt.Sprintf("%d new Silver examples (threshold %d)", signals.NewSilverSinceLastRun, cfg.MinNewSilverExamples))
	}
	if cfg.MaxDisagreementRate > 0 && signals.DisagreementRate > cfg.MaxDisagreementRate {
		reasons = append(reasons, fmt.Sprintf("disagreement rate %.3f exceeds max %.3f", signals.DisagreementRate, cfg.MaxDisagreementRate))
	}
	if cfg.MaxManualErrorRate > 0 && signals.ManualErrorRate > cfg.MaxManualErrorRate {
		reasons = append(reasons, fmt.Sprintf("manual error rate %.3f exceeds max %.3f", signals.ManualErrorRate, cfg.MaxManualErrorRate))
	}
	if cfg.NovelVocabularyThreshold > 0 && signals.NovelVocabularyCount >= cfg.NovelVocabularyThreshold {
		reasons = append(reasons, fmt.Sprintf("%d novel-vocabulary queue items (threshold %d)", signals.NovelVocabularyCount, cfg.NovelVocabularyThreshold))
	}
	if cfg.MinHardExampleCount > 0 && signals.HardExampleCount >= cfg.MinHardExampleCount {
		reasons = append(reasons, fmt.Sprintf("%d hard-example candidates (threshold %d)", signals.HardExampleCount, cfg.MinHardExampleCount))
	}
	return Result{Triggered: len(reasons) > 0, Reasons: reasons}
}
