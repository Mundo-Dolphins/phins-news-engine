// Package dedupeval offers offline scoring of a hand-labeled dataset of
// article-title pairs against the configured near-duplicate/same-event
// thresholds, so they can be calibrated with real examples without
// touching code. See the phins-news-engine-tools "dedup evaluate" command.
package dedupeval

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/similarity"
)

// PairArticle is one side of a labeled comparison pair.
type PairArticle struct {
	Title        string    `yaml:"title"`
	CanonicalURL string    `yaml:"canonical_url"`
	SourceType   string    `yaml:"source_type"`
	ContentType  string    `yaml:"content_type"`
	PublishedAt  time.Time `yaml:"published_at"`
}

// Pair is a labeled comparison between two articles.
type Pair struct {
	A     PairArticle `yaml:"a"`
	B     PairArticle `yaml:"b"`
	Label string      `yaml:"label"` // same_content | same_event | different | ambiguous
}

// Dataset is a labeled set of comparison pairs.
type Dataset struct {
	Pairs []Pair `yaml:"pairs"`
}

// LoadDataset reads and parses a YAML dataset file.
func LoadDataset(path string) (Dataset, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided CLI flag, not user input
	if err != nil {
		return Dataset{}, fmt.Errorf("dedupeval: read %s: %w", path, err)
	}
	var dataset Dataset
	if err := yaml.Unmarshal(data, &dataset); err != nil {
		return Dataset{}, fmt.Errorf("dedupeval: parse %s: %w", path, err)
	}
	return dataset, nil
}

// BinaryMetrics is a confusion-matrix-derived precision/recall/F1 view.
type BinaryMetrics struct {
	TP, FP, FN, TN int
}

// Precision returns TP/(TP+FP), or 0 when the denominator is 0.
func (m BinaryMetrics) Precision() float64 {
	if m.TP+m.FP == 0 {
		return 0
	}
	return float64(m.TP) / float64(m.TP+m.FP)
}

// Recall returns TP/(TP+FN), or 0 when the denominator is 0.
func (m BinaryMetrics) Recall() float64 {
	if m.TP+m.FN == 0 {
		return 0
	}
	return float64(m.TP) / float64(m.TP+m.FN)
}

// F1 returns the harmonic mean of Precision and Recall, or 0 when both are 0.
func (m BinaryMetrics) F1() float64 {
	p, r := m.Precision(), m.Recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

func (m *BinaryMetrics) record(predictedRelated, goldRelated bool) {
	switch {
	case predictedRelated && goldRelated:
		m.TP++
	case predictedRelated && !goldRelated:
		m.FP++
	case !predictedRelated && goldRelated:
		m.FN++
	default:
		m.TN++
	}
}

// Report is the full output of Evaluate.
type Report struct {
	Overall       BinaryMetrics
	BySourceType  map[string]BinaryMetrics
	ByContentType map[string]BinaryMetrics
	Confusion     map[string]map[string]int // predicted band -> gold label -> count
	Excluded      int
}

// Evaluate scores every non-ambiguous pair in dataset: normalizes both
// titles, computes their similarity score, bands it (near_duplicate /
// same_event / none) using the same double-threshold logic ProcessCycle
// uses, and compares the collapsed band ("related"/"not_related") against
// the collapsed gold label. Ambiguous pairs are counted in Excluded and
// skipped entirely.
func Evaluate(dataset Dataset, textCfg normalization.TextConfig, thresholdCfg deduplication.ThresholdConfig, method string) Report {
	report := Report{
		BySourceType:  map[string]BinaryMetrics{},
		ByContentType: map[string]BinaryMetrics{},
		Confusion:     map[string]map[string]int{},
	}

	for _, pair := range dataset.Pairs {
		if pair.Label == "ambiguous" {
			report.Excluded++
			continue
		}

		a := normalization.Normalize(pair.A.Title, textCfg)
		b := normalization.Normalize(pair.B.Title, textCfg)
		var score float64
		if method == "dice" {
			score = similarity.Dice(a.Shingles, b.Shingles)
		} else {
			score = similarity.Jaccard(a.Shingles, b.Shingles)
		}

		tokenCount := len(a.Tokens)
		if len(b.Tokens) < tokenCount {
			tokenCount = len(b.Tokens)
		}
		thresholds := deduplication.ResolveThresholds(thresholdCfg, pair.A.SourceType, tokenCount)

		band := "none"
		switch {
		case score >= thresholds.DuplicateThreshold:
			band = "near_duplicate"
		case score >= thresholds.EventThreshold:
			band = "same_event"
		}

		predictedRelated := band != "none"
		goldRelated := pair.Label == "same_content" || pair.Label == "same_event"

		report.Overall.record(predictedRelated, goldRelated)
		recordInto(report.BySourceType, pair.A.SourceType, predictedRelated, goldRelated)
		recordInto(report.ByContentType, pair.A.ContentType, predictedRelated, goldRelated)

		if report.Confusion[band] == nil {
			report.Confusion[band] = map[string]int{}
		}
		report.Confusion[band][pair.Label]++
	}

	return report
}

func recordInto(m map[string]BinaryMetrics, key string, predictedRelated, goldRelated bool) {
	metrics := m[key]
	metrics.record(predictedRelated, goldRelated)
	m[key] = metrics
}
