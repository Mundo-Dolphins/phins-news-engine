// Package activelearning ranks candidate articles for human editorial
// review, prioritizing the ones most likely to improve the classifier if
// labeled: the ambiguous zone, model/rule disagreement, novel sources,
// underrepresented entities, and staleness. It never samples randomly.
//
// Weights below are a documented starting point, not a calibrated result
// — the same caveat that applies to the seed editorial model's
// own thresholds: there is no real labeled history yet to calibrate
// against.
package activelearning

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Signals is every per-candidate input Score combines. Each field defaults
// meaningfully to its Go zero value: no prior event, an unseen source, no
// labeling history for that source.
type Signals struct {
	HasPriorEvent  bool
	EventScore     float64
	EventThreshold float64
	EventType      string // "" | "editorial_shadow" | "editorial_ambiguous" | "editorial"

	SourceLabelCount int
	EntityLabelCount int

	HasSourceLabelHistory bool
	DaysSinceSourceLabel  float64
}

const (
	ambiguityWeight     = 3.0
	disagreementBonus   = 2.0
	sourceNoveltyWeight = 2.0
	entityNoveltyWeight = 1.0
	stalenessWeight     = 0.02 // per day, capped below
	stalenessCapDays    = 60.0
)

// Score combines s into a single non-negative priority — higher means
// "review this sooner." It has no I/O and no dependency on any port.
func Score(s Signals) float64 {
	var total float64

	if s.HasPriorEvent {
		distance := s.EventScore - s.EventThreshold
		if distance < 0 {
			distance = -distance
		}
		// Closer to the threshold (distance near 0) scores higher; a
		// distance of 50 or more contributes nothing.
		closeness := 1 - distance/50
		if closeness < 0 {
			closeness = 0
		}
		total += ambiguityWeight * closeness
	}
	if s.EventType == "editorial_ambiguous" {
		total += disagreementBonus
	}

	// Novelty is highest at 0 prior labels and decays to 0 by 20 labels.
	sourceNovelty := 1 - float64(s.SourceLabelCount)/20
	if sourceNovelty < 0 {
		sourceNovelty = 0
	}
	total += sourceNoveltyWeight * sourceNovelty

	entityNovelty := 1 - float64(s.EntityLabelCount)/20
	if entityNovelty < 0 {
		entityNovelty = 0
	}
	total += entityNoveltyWeight * entityNovelty

	if s.HasSourceLabelHistory {
		staleDays := s.DaysSinceSourceLabel
		if staleDays > stalenessCapDays {
			staleDays = stalenessCapDays
		}
		total += stalenessWeight * staleDays
	}

	return total
}

// Deps are the ports Select reads from.
type Deps struct {
	Articles        out.ArticleRepository
	Labels          out.HumanLabelRepository
	Events          out.DecisionRepository
	ReviewDecisions out.ReviewDecisionRepository
	Clock           out.Clock
	// ExcludedSourceIDs are governed by source policy rather than the model.
	ExcludedSourceIDs map[string]bool
}

// Options configures one Select call.
type Options struct {
	// Since bounds ListCandidatesForReview — only articles fetched at or
	// after this time are considered.
	Since time.Time
	// PoolLimit bounds how many raw candidates are pulled from storage
	// before scoring (keeps scoring itself, and its per-candidate event
	// lookups, bounded regardless of table size).
	PoolLimit int
	// SampleSize caps how many ranked candidates Select returns.
	SampleSize int
	// AmbiguousCooldown excludes an article from selection for this long
	// after it was marked "ambiguous" in any session.
	AmbiguousCooldown time.Duration
}

// Candidate is one scored, ranked article ready for the review screen.
type Candidate struct {
	Article article.Article
	Score   float64
	Signals Signals
}

// Select builds the ranked review queue: pulls a bounded candidate pool,
// excludes articles under the ambiguous cooldown, scores every remaining
// candidate against the existing labeled set's source/entity coverage and
// its most recent editorial-classifier event, ranks descending, and caps
// at opts.SampleSize.
func Select(ctx context.Context, deps Deps, opts Options) ([]Candidate, error) {
	pool, err := deps.Articles.ListCandidatesForReview(ctx, opts.Since, opts.PoolLimit)
	if err != nil {
		return nil, fmt.Errorf("activelearning: list candidates: %w", err)
	}

	cooldownSince := deps.Clock.Now().Add(-opts.AmbiguousCooldown)
	cooldownIDs, err := deps.ReviewDecisions.ListRecentByAction(ctx, "ambiguous", cooldownSince)
	if err != nil {
		return nil, fmt.Errorf("activelearning: list ambiguous cooldown: %w", err)
	}
	excluded := make(map[string]bool, len(cooldownIDs))
	for _, id := range cooldownIDs {
		excluded[id] = true
	}

	labels, err := deps.Labels.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("activelearning: list labels: %w", err)
	}
	sourceCounts := map[string]int{}
	entityCounts := map[string]int{}
	sourceLastLabeled := map[string]time.Time{}
	for _, ex := range labels {
		a, findErr := deps.Articles.FindByID(ctx, ex.ArticleID)
		if findErr != nil {
			continue // an orphaned label shouldn't fail selection — mirrors train export's own skip-on-missing-article behavior
		}
		sourceCounts[a.Source.ID.String()]++
		if a.PrimaryEntity != "" {
			entityCounts[a.PrimaryEntity]++
		}
		if last, ok := sourceLastLabeled[a.Source.ID.String()]; !ok || ex.LabeledAt.After(last) {
			sourceLastLabeled[a.Source.ID.String()] = ex.LabeledAt
		}
	}

	now := deps.Clock.Now()
	candidates := make([]Candidate, 0, len(pool))
	for _, a := range pool {
		if excluded[a.ID] || deps.ExcludedSourceIDs[a.Source.ID.String()] {
			continue
		}

		signals := Signals{
			SourceLabelCount: sourceCounts[a.Source.ID.String()],
			EntityLabelCount: entityCounts[a.PrimaryEntity],
		}
		if last, ok := sourceLastLabeled[a.Source.ID.String()]; ok {
			signals.HasSourceLabelHistory = true
			signals.DaysSinceSourceLabel = now.Sub(last).Hours() / 24
		}

		events, eventsErr := deps.Events.ListByArticle(ctx, a.ID)
		if eventsErr != nil {
			return nil, fmt.Errorf("activelearning: list events for %s: %w", a.ID, eventsErr)
		}
		if latest := latestEditorialEvent(events); latest != nil {
			signals.HasPriorEvent = true
			signals.EventScore = latest.Score
			signals.EventThreshold = latest.Threshold
			signals.EventType = latest.EventType
		}

		candidates = append(candidates, Candidate{Article: a, Score: Score(signals), Signals: signals})
	}

	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if len(candidates) > opts.SampleSize {
		candidates = candidates[:opts.SampleSize]
	}
	return candidates, nil
}

// latestEditorialEvent returns the most recent editorial-classifier event
// (event_type "editorial_shadow"|"editorial_ambiguous"|"editorial") in
// events, or nil if there is none — events is assumed oldest-first, the
// order ListByArticle documents.
func latestEditorialEvent(events []out.Event) *out.Event {
	var latest *out.Event
	for i := range events {
		switch events[i].EventType {
		case "editorial_shadow", "editorial_ambiguous", "editorial":
			e := events[i]
			latest = &e
		}
	}
	return latest
}
