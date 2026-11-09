// Package editorialmetrics computes read-only reports over editorial
// decisions: canary-criteria evaluation, decision-volume metrics, the
// recent-decision review and the deterministic-veto dry run. It is shared
// by the `editorial` CLI commands and the admin-panel actions; nothing
// here writes.
package editorialmetrics

import (
	"context"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// CanaryResult is canary status's pass/fail evaluation against
// EditorialRolloutConfig's canary_* fields. ArticleCount and ElapsedDays
// are computed from real enforced-arm decisions only (reason codes
// "editorial:canary_reject" and "editorial:canary_ambiguous_reject",
// plus terminal "editorial_veto" rejections),
// counted and dated across the enforced arm's entire recorded history —
// these are the only two gates actually implemented. Precision and
// FalsePositiveRate are left at their zero value: computing them
// requires a labeled ground truth that article_processing_events alone
// doesn't carry (see EvaluateCanary's doc comment).
type CanaryResult struct {
	Passed            bool
	Failures          []string
	ArticleCount      int
	ElapsedDays       int
	Precision         float64
	FalsePositiveRate float64
}

// CanaryCoverageNote is printed alongside every status/promote
// verdict so a reader never mistakes an unevaluated criterion for a
// measured one: `editorial_rollout` configures four gates, but only two
// are computable from article_processing_events alone.
const CanaryCoverageNote = "(2 of 4 configured criteria evaluated: article count, elapsed days; precision and false-positive-rate require labeled ground truth not yet available)"

// EvaluateCanary's Precision/FalsePositiveRate fields need a
// labeled ground truth to compute against, which article_processing_events
// alone doesn't carry — they are left at their zero value with this
// comment noting they require cross-referencing human_labels/reconciled
// outcomes, a known limitation documented in the runbook (§ Task 14)
// rather than a TDD gap: only the count/day gates are implemented for
// real here.
//
// The event query below is deliberately NOT bounded by CanaryMinDays:
// ArticleCount/ElapsedDays must reflect the true cumulative history of
// the enforced arm since canary mode actually started, not merely
// whatever falls inside a window whose own width is CanaryMinDays — a
// query bounded that way could never observe more than CanaryMinDays of
// elapsed time in the first place, making the day-gate structurally
// unsatisfiable (a bug caught in review). ListByEventTypesSince's since
// argument is instead the zero time.Time, i.e. "since the beginning of
// recorded history"; CanaryMinDays/CanaryMinArticles are used only in
// the threshold comparisons below.
func EvaluateCanary(ctx context.Context, decisions out.DecisionRepository, rollout config.EditorialRolloutConfig, now time.Time) (CanaryResult, error) {
	events, err := decisions.ListByEventTypesSince(ctx, []string{"editorial", "editorial_veto", "editorial_canary_shadow", "editorial_canary_shadow_would_reject"}, time.Time{}, 1000000)
	if err != nil {
		return CanaryResult{}, fmt.Errorf("editorial canary: list events: %w", err)
	}
	var enforced int
	var earliestEnforced time.Time
	for _, e := range events {
		if e.Mode != "canary" {
			continue
		}
		// The enforced arm's real decisions are the score/ambiguous
		// rejects plus veto-triggered rejects — the veto gate fires
		// before the score is ever consulted, so omitting it would
		// undercount enforced-arm activity and bias both gates
		// optimistically. A veto event is terminal (and thus
		// enforced-arm) only when it actually moved the article to
		// rejected_quality; appendEditorialVetoEvent's log-only twin
		// leaves ToStatus at the article's current status.
		terminalVeto := e.EventType == "editorial_veto" && e.ToStatus == article.StatusRejectedQuality
		if !terminalVeto && e.ReasonCode != "editorial:canary_reject" && e.ReasonCode != "editorial:canary_ambiguous_reject" {
			continue // shadow-arm event within canary mode (e.g. editorial_canary_shadow*), not part of the enforced-arm sample
		}
		enforced++
		if earliestEnforced.IsZero() || e.OccurredAt.Before(earliestEnforced) {
			earliestEnforced = e.OccurredAt
		}
	}
	result := CanaryResult{ArticleCount: enforced}
	if !earliestEnforced.IsZero() {
		result.ElapsedDays = int(now.Sub(earliestEnforced).Hours() / 24)
	}
	var failures []string
	if enforced < rollout.CanaryMinArticles {
		failures = append(failures, fmt.Sprintf("article count %d < min %d", enforced, rollout.CanaryMinArticles))
	}
	if result.ElapsedDays < rollout.CanaryMinDays {
		failures = append(failures, fmt.Sprintf("elapsed days %d < min %d", result.ElapsedDays, rollout.CanaryMinDays))
	}
	result.Passed = len(failures) == 0
	result.Failures = failures
	return result, nil
}

// metricsEventTypes is every event_type the metrics report aggregates
// over — kept as an explicit allowlist so a future unrelated event_type
// added elsewhere never silently skews these numbers.
var metricsEventTypes = []string{
	"editorial", "editorial_shadow", "editorial_ambiguous",
	"editorial_canary_shadow", "editorial_canary_shadow_would_reject",
}

// MetricsReport is editorial metrics' output shape, printed as text or
// (--json) as JSON. Score/reason fields only ever carry aggregate
// counts and IDs — never article title/summary text.
//
// There is deliberately no per-source breakdown here: out.Event carries
// no source ID, so a by-source reject rate would require a new join
// against the articles table that this report does not otherwise need.
type MetricsReport struct {
	Since         time.Time      `json:"since"`
	Total         int            `json:"total"`
	ByMode        map[string]int `json:"by_mode"`
	ByReasonCode  map[string]int `json:"by_reason_code"`
	RejectRate    float64        `json:"reject_rate"`
	AmbiguousRate float64        `json:"ambiguous_rate"`
	ByScoreDecile [10]int        `json:"by_score_decile"`
}

// Metrics aggregates the editorial decisions recorded at or after since
// into decision volume, zone rates and the score distribution.
func Metrics(ctx context.Context, decisions out.DecisionRepository, since time.Time) (MetricsReport, error) {
	events, err := decisions.ListByEventTypesSince(ctx, metricsEventTypes, since, 100000)
	if err != nil {
		return MetricsReport{}, fmt.Errorf("editorial metrics: list events: %w", err)
	}
	report := MetricsReport{Since: since, ByMode: map[string]int{}, ByReasonCode: map[string]int{}}
	var rejected, ambiguous int
	for _, e := range events {
		report.Total++
		report.ByMode[e.Mode]++
		if e.ReasonCode != "" {
			report.ByReasonCode[e.ReasonCode]++
		}
		// Terminal rejects (score, ambiguous, canary-enforced-arm) all
		// share EventType "editorial" (see rejectEditorialScored) and
		// are distinguished only by ReasonCode — classify on ReasonCode,
		// never EventType, for these. "editorial_ambiguous" EventType is
		// the distinct non-terminal case: AmbiguousPolicy=accept logged
		// it without rejecting, so it counts toward AmbiguousRate but
		// not RejectRate.
		switch {
		case e.ReasonCode == "editorial:reject_score" || e.ReasonCode == "editorial:canary_reject":
			rejected++
		case e.ReasonCode == "editorial:ambiguous_reject" || e.ReasonCode == "editorial:canary_ambiguous_reject":
			rejected++
			ambiguous++
		case e.EventType == "editorial_ambiguous":
			ambiguous++
		}
		decile := int(e.Score / 10)
		if decile > 9 {
			decile = 9
		}
		if decile >= 0 {
			report.ByScoreDecile[decile]++
		}
	}
	if report.Total > 0 {
		report.RejectRate = float64(rejected) / float64(report.Total)
		report.AmbiguousRate = float64(ambiguous) / float64(report.Total)
	}
	return report, nil
}

// ReviewEventTypes are the event_type values ProcessCycle (Task
// 25) writes for editorial decisions: shadow-mode audits, enforce-mode
// ambiguous-zone flags, and enforce-mode rejects. The review report
// counts them in this order.
var ReviewEventTypes = []string{"editorial_shadow", "editorial_ambiguous", "editorial"}

// ArticleFinder is the slice of out.ArticleRepository the review report
// needs: looking an article up by ID.
type ArticleFinder interface {
	// FindByID returns the article with the given ID, or an error if none
	// exists.
	FindByID(ctx context.Context, id string) (article.Article, error)
}

// ReviewRow is one editorial decision next to its article's actual
// outcome.
type ReviewRow struct {
	// OccurredAt is when the decision was recorded.
	OccurredAt time.Time
	// EventType is the decision's event_type (one of ReviewEventTypes).
	EventType string
	// Score is the classifier score of the decision.
	Score float64
	// ActualStatus is the article's current status, "(unknown)" when the
	// article is gone.
	ActualStatus string
	// Title is the article's title, "(article not found)" when it is gone.
	Title string
}

// ReviewReport is the recent-decision review: per-event-type counts and
// one row per decision, most recent first.
type ReviewReport struct {
	// Counts maps each event type to its number of decisions.
	Counts map[string]int
	// Rows lists every decision returned, most recent first.
	Rows []ReviewRow
}

// Review lists up to limit editorial decisions recorded at or after
// since, each with its article's current status and title. A decision
// whose article no longer exists keeps its row with placeholder values.
func Review(ctx context.Context, decisions out.DecisionRepository, articles ArticleFinder, since time.Time, limit int) (ReviewReport, error) {
	events, err := decisions.ListByEventTypesSince(ctx, ReviewEventTypes, since, limit)
	if err != nil {
		return ReviewReport{}, err
	}
	report := ReviewReport{Counts: map[string]int{}, Rows: make([]ReviewRow, 0, len(events))}
	for _, e := range events {
		report.Counts[e.EventType]++
		title, status := "(article not found)", "(unknown)"
		if a, err := articles.FindByID(ctx, e.ArticleID); err == nil {
			title, status = a.Title, string(a.Status)
		}
		report.Rows = append(report.Rows, ReviewRow{
			OccurredAt: e.OccurredAt, EventType: e.EventType, Score: e.Score,
			ActualStatus: status, Title: title,
		})
	}
	return report, nil
}

// VetoImpactReport is the deterministic-veto dry run over an exported
// dataset: how many examples the veto rules would block, per reason code
// with up to five example article IDs each.
type VetoImpactReport struct {
	Dataset   string              `json:"dataset"`
	Total     int                 `json:"total"`
	Blocked   int                 `json:"blocked"`
	Reduction float64             `json:"reduction"`
	ByReason  map[string]int      `json:"by_reason_code"`
	Examples  map[string][]string `json:"examples_by_reason_code"`
}

// VetoImpact evaluates every veto rule of cfg against each example's
// title and body. dataset is only echoed into the report.
func VetoImpact(examples []editorialtrain.TrainingExample, cfg editorialveto.Config, dataset string) VetoImpactReport {
	report := VetoImpactReport{Dataset: dataset, Total: len(examples), ByReason: map[string]int{}, Examples: map[string][]string{}}
	for _, example := range examples {
		matches := editorialveto.EvaluateAll(example.Title+" "+example.Body, cfg)
		if len(matches) == 0 {
			continue
		}
		report.Blocked++
		// Blocked is unique-article reduction. Per-rule counts include every
		// matching rule so operators can see the independent impact of each
		// rule; online processing still uses the first-match-wins result.
		for _, match := range matches {
			code := match.ReasonCode
			report.ByReason[code]++
			if len(report.Examples[code]) < 5 {
				report.Examples[code] = append(report.Examples[code], example.ArticleID)
			}
		}
	}
	if report.Total > 0 {
		report.Reduction = float64(report.Blocked) / float64(report.Total)
	}
	return report
}
