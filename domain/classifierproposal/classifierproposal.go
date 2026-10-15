// Package classifierproposal models pending, review-gated changes to the
// team relevance classifier dictionary.
package classifierproposal

import (
	"errors"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

var ErrNotFound = errors.New("classifierproposal: not found")

type Kind string

const (
	KindTerm           Kind = "classifier_term"
	KindAlias          Kind = "classifier_alias"
	KindTermPromotion  Kind = "classifier_term_promotion"
	KindTermRemoval    Kind = "classifier_term_removal"
	KindNegativeTerm   Kind = "classifier_negative_term"
	KindContextualTerm Kind = "classifier_contextual_term"
	KindWeightChange   Kind = "classifier_weight_change"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

type Source struct {
	ArticleID    string
	ArticleTitle string
}

// Proposal is one pending dictionary change, aggregated across repeated
// evidence until a human approves or rejects it.
type Proposal struct {
	ID              string
	Kind            Kind
	Category        string
	Value           string
	NormalizedValue string
	Operation       string
	TargetEntry     string
	Metadata        map[string]string
	Confidence      float64
	Occurrences     int
	Sources         []Source
	Status          Status
	FirstSeen       time.Time
	LastSeen        time.Time
	AnalyzerVersion string
	CreatedAt       time.Time
	DecidedAt       time.Time
}

func Normalize(value string) string {
	return entityproposal.Normalize(value)
}

func Score(p Proposal) float64 {
	titleHits := 0
	norm := Normalize(p.Value)
	for _, s := range p.Sources {
		if norm != "" && strings.Contains(Normalize(s.ArticleTitle), norm) {
			titleHits++
		}
	}
	return float64(p.Occurrences) + 1.5*float64(len(p.Sources)) + float64(titleHits) + p.Confidence
}
