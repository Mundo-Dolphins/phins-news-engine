// domain/reviewpriority/reviewpriority.go
//
// Package reviewpriority computes a deterministic priority score for the
// offline LLM teacher's review queue.
// Score is a pure function over already-known facts: the production
// Naive Bayes classifier's prediction, the LLM's independent proposal,
// and cheap evidence-table lookups (novel vocabulary, unknown entities,
// source/article recency). The LLM never computes this score, and Score
// itself performs no I/O — all evidence must already be gathered into
// Input before calling it.
package reviewpriority

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// Signals bundles the evidence-table lookups Score needs beyond the NB
// prediction and LLM proposal themselves.
type Signals struct {
	// UnknownEntityCount is how many unknown_entity_occurrences rows
	// reference this article.
	UnknownEntityCount int
	// PendingDictionaryProposalCount is how many pending
	// classifier_dictionary_proposals were derived (in part) from this
	// article.
	PendingDictionaryProposalCount int
	// SourceFirstSeenKnown is false when the article's source has no
	// recorded first_seen_at — the new_source component is then always 0.
	SourceFirstSeenKnown bool
	SourceFirstSeen      time.Time
	// HardMiningCount is 1 if this article has an unresolved
	// hard_example_candidates row (application/trainmine),
	// 0 otherwise.
	HardMiningCount int
}

// Weights are the per-component multipliers applied before summing.
type Weights struct {
	Disagreement    float64
	NBUncertainty   float64
	LLMUncertainty  float64
	NovelVocabulary float64
	UnknownEntities float64
	NewSource       float64
	Recency         float64
	HardMining      float64
}

// Config is everything Score needs beyond the per-article inputs —
// mirrors config.ReviewScoringConfig one-to-one; the CLI wiring layer
// converts between them.
type Config struct {
	Weights                         Weights
	LLMConfidenceAutoLabelThreshold float64
	LLMConfidenceUncertainThreshold float64
	NewSourceWindowHours            float64
	RecencyWindowHours              float64
}

// Input is one article's full set of facts Score needs.
type Input struct {
	NBZone          editorial.Zone
	NBValue         float64
	NBThresholds    editorial.Thresholds
	LLMLabel        string // "publish" | "discard" | "uncertain"
	LLMConfidence   float64
	ArticleAgeHours float64
	Signals         Signals
	Now             time.Time
}

// Result is Score's output: whether the article should be skipped
// entirely (an "obvious agreement"), and if not, its priority score and
// a human-readable explanation of the largest contributors.
type Result struct {
	Skip   bool
	Score  float64
	Reason string
}

// QueueEntry is one persisted review_queue row — the shape
// reviewqueueport.ReviewQueueRepository reads and writes.
type QueueEntry struct {
	ArticleID      string
	ProposalID     string
	Score          float64
	Reason         string
	NBZone         editorial.Zone
	NBValue        float64
	LLMLabel       string
	LLMConfidence  float64
	SuggestedLabel string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const (
	// novelVocabularyCap and unknownEntitiesCap saturate their raw counts
	// to a 0..1 signal — a reasonable starting point per the design
	// spec's "Fuera de alcance" (no dedicated config surface for these).
	novelVocabularyCap = 3.0
	unknownEntitiesCap = 5.0
)

// llmPolarity maps an LLM label onto the NB Zone polarity it agrees
// with, or "" for "uncertain" (no polarity — never counts as agreement).
func llmPolarity(label string) editorial.Zone {
	switch label {
	case "publish":
		return editorial.ZoneAccept
	case "discard":
		return editorial.ZoneReject
	default:
		return ""
	}
}

// isObviousAgreement is true only when the NB zone is confident (not
// ambiguous), its polarity matches the LLM's label, and the LLM itself
// was confident (>= cfg.LLMConfidenceAutoLabelThreshold). Obvious
// agreements must never enter the review queue.
func isObviousAgreement(in Input, cfg Config) bool {
	if in.NBZone != editorial.ZoneReject && in.NBZone != editorial.ZoneAccept {
		return false
	}
	if llmPolarity(in.LLMLabel) != in.NBZone {
		return false
	}
	return in.LLMConfidence >= cfg.LLMConfidenceAutoLabelThreshold
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func saturate(count int, cap float64) float64 {
	if cap <= 0 {
		return 0
	}
	return clamp01(float64(count) / cap)
}

type component struct {
	name   string
	value  float64 // 0..1 normalized signal, before weighting
	weight float64
}

func (c component) contribution() float64 { return c.value * c.weight }

// Score computes one article's review-priority score.
func Score(in Input, cfg Config) Result {
	if isObviousAgreement(in, cfg) {
		return Result{Skip: true}
	}

	disagreement := 0.5
	if in.NBZone != editorial.ZoneAmbiguous {
		switch pol := llmPolarity(in.LLMLabel); pol {
		case "":
			disagreement = 0.5
		case in.NBZone:
			disagreement = 0
		default:
			disagreement = 1
		}
	}

	// nbUncertainty peaks at the midpoint between Reject and Accept
	// (maximally undecided), decaying to 0 at either threshold edge and
	// beyond -- NOT the same as "distance to nearest threshold"; a
	// value near an edge is confidently classified, not uncertain. See
	// TestScore_NBUncertaintyPeaksAtThresholdMidpoint.
	nbUncertainty := 0.0
	if halfBand := (in.NBThresholds.Accept - in.NBThresholds.Reject) / 2; halfBand > 0 {
		mid := (in.NBThresholds.Reject + in.NBThresholds.Accept) / 2
		nbUncertainty = clamp01(1 - absFloat(in.NBValue-mid)/halfBand)
	}

	llmUncertainty := 0.0
	if band := cfg.LLMConfidenceAutoLabelThreshold - cfg.LLMConfidenceUncertainThreshold; band > 0 {
		llmUncertainty = 1 - clamp01((in.LLMConfidence-cfg.LLMConfidenceUncertainThreshold)/band)
	}

	novelVocabulary := saturate(in.Signals.PendingDictionaryProposalCount, novelVocabularyCap)
	unknownEntities := saturate(in.Signals.UnknownEntityCount, unknownEntitiesCap)

	newSource := 0.0
	if in.Signals.SourceFirstSeenKnown && cfg.NewSourceWindowHours > 0 {
		ageHours := in.Now.Sub(in.Signals.SourceFirstSeen).Hours()
		if ageHours < 0 {
			ageHours = 0
		}
		newSource = clamp01(1 - ageHours/cfg.NewSourceWindowHours)
	}

	recency := 0.0
	if cfg.RecencyWindowHours > 0 {
		ageHours := in.ArticleAgeHours
		if ageHours < 0 {
			ageHours = 0
		}
		recency = clamp01(1 - ageHours/cfg.RecencyWindowHours)
	}

	hardMining := 0.0
	if in.Signals.HardMiningCount > 0 {
		hardMining = 1.0
	}

	components := []component{
		{"disagreement", disagreement, cfg.Weights.Disagreement},
		{"nb_uncertainty", nbUncertainty, cfg.Weights.NBUncertainty},
		{"llm_uncertainty", llmUncertainty, cfg.Weights.LLMUncertainty},
		{"novel_vocabulary", novelVocabulary, cfg.Weights.NovelVocabulary},
		{"unknown_entities", unknownEntities, cfg.Weights.UnknownEntities},
		{"new_source", newSource, cfg.Weights.NewSource},
		{"recency", recency, cfg.Weights.Recency},
		{"hard_mining", hardMining, cfg.Weights.HardMining},
	}

	total := 0.0
	for _, c := range components {
		total += c.contribution()
	}

	return Result{Score: total, Reason: buildReason(components)}
}

// buildReason describes the (up to 3) largest non-zero contributors,
// most significant first, e.g. "disagreement=1.00; unknown_entities=0.40".
func buildReason(components []component) string {
	sorted := make([]component, len(components))
	copy(sorted, components)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].contribution() > sorted[j].contribution() })

	var parts []string
	for _, c := range sorted {
		if c.contribution() <= 0.01 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%.2f", c.name, c.contribution()))
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return "no significant signal"
	}
	return strings.Join(parts, "; ")
}
