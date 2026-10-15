// Package regressioncheck compares classification outcomes for two
// dictionary versions against a shared, fixed regression dataset — a
// safety net for promoting a candidate dictionary: it never reimplements
// relevance classification itself, only drives the real
// dictionary.Prepare matcher and classification.Decide twice (once per
// dictionary) and reports where the two disagree.
package regressioncheck

import (
	"errors"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification/regressiondata"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

// Flip records one regression case whose classification.Decide outcome
// changed between the previous and candidate dictionary.
type Flip struct {
	CaseName    string
	Direction   string // "accept_to_reject" or "reject_to_accept"
	ScoreBefore float64
	ScoreAfter  float64
	RuleBefore  string
	RuleAfter   string
}

// Report summarizes how many of the embedded regression dataset's cases
// flipped Related outcome between previous and candidate.
type Report struct {
	TotalCases int
	FlipCount  int
	Flips      []Flip
}

// ErrCasesDoNotFitDictionary reports that none of the regression cases
// expected to be related classify as related under the previous dictionary:
// the dataset does not describe the team that dictionary covers, so every
// comparison would trivially find no flips.
var ErrCasesDoNotFitDictionary = errors.New("regressioncheck: none of the want_related regression cases is related under the previous dictionary; " +
	"the regression dataset does not fit this dictionary (a host program must install its own cases with regressiondata.SetCases or SetCasesJSON)")

// Run compares classification outcomes for previous vs. candidate on the
// embedded seed regression dataset, using the real dictionary.Prepare
// matcher and classification.Decide — no reimplementation of relevance
// classification.
func Run(previous, candidate dictionary.Dictionary) (Report, error) {
	prevMatcher, err := dictionary.Prepare(previous)
	if err != nil {
		return Report{}, fmt.Errorf("regressioncheck: prepare previous: %w", err)
	}
	candMatcher, err := dictionary.Prepare(candidate)
	if err != nil {
		return Report{}, fmt.Errorf("regressioncheck: prepare candidate: %w", err)
	}

	cases, err := regressiondata.Cases()
	if err != nil {
		return Report{}, fmt.Errorf("regressioncheck: %w", err)
	}

	report := Report{TotalCases: len(cases)}
	wantRelated, relatedBefore := 0, 0
	for _, c := range cases {
		before := classification.Decide(c.Title, c.Body, prevMatcher)
		after := classification.Decide(c.Title, c.Body, candMatcher)
		if c.WantRelated {
			wantRelated++
			if before.Related {
				relatedBefore++
			}
		}
		if before.Related == after.Related {
			continue
		}
		direction := "reject_to_accept"
		if before.Related && !after.Related {
			direction = "accept_to_reject"
		}
		report.FlipCount++
		report.Flips = append(report.Flips, Flip{
			CaseName: c.Name, Direction: direction,
			ScoreBefore: before.Score, ScoreAfter: after.Score,
			RuleBefore: before.RuleApplied, RuleAfter: after.RuleApplied,
		})
	}
	if wantRelated > 0 && relatedBefore == 0 {
		return report, ErrCasesDoNotFitDictionary
	}
	return report, nil
}
