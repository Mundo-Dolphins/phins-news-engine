// Package decisionaudit reads the article_processing_events audit trail
// (ADR 0005) for the `audit duplicates|quality` reports, shared by the CLI
// and the admin actions.
package decisionaudit

import (
	"context"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Placeholders for decisions whose article row no longer exists.
const (
	// MissingTitle is the Row title when the article is gone.
	MissingTitle = "(article not found)"
	// MissingStatus is the Row status when the article is gone.
	MissingStatus = "(unknown)"
)

var (
	duplicateTypes = []string{"dedup"}
	qualityTypes   = []string{"editorial_shadow", "editorial_ambiguous", "editorial"}
)

// Row is one audited decision joined with its article.
type Row struct {
	OccurredAt time.Time
	ArticleID  string
	// Title and Status come from the article, or the Missing* placeholders.
	Title, Status string
	// Detail is the decision's recorded reason detail.
	Detail string
	// Score and Threshold are the editorial score and the accept threshold
	// that gated it.
	Score, Threshold float64
	// BelowThreshold is true when Score fell below Threshold.
	BelowThreshold bool
}

// Duplicates lists up to limit dedup rejections since the given time.
func Duplicates(ctx context.Context, decisions appout.DecisionRepository, articles appout.ArticleRepository, since time.Time, limit int) ([]Row, error) {
	rows, _, err := list(ctx, decisions, articles, duplicateTypes, since, limit)
	return rows, err
}

// Quality lists up to limit editorial decisions since the given time and
// returns how many of them scored below their gating threshold.
func Quality(ctx context.Context, decisions appout.DecisionRepository, articles appout.ArticleRepository, since time.Time, limit int) ([]Row, int, error) {
	return list(ctx, decisions, articles, qualityTypes, since, limit)
}

func list(ctx context.Context, decisions appout.DecisionRepository, articles appout.ArticleRepository, types []string, since time.Time, limit int) ([]Row, int, error) {
	events, err := decisions.ListByEventTypesSince(ctx, types, since, limit)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]Row, 0, len(events))
	below := 0
	for _, e := range events {
		title, status := MissingTitle, MissingStatus
		if a, err := articles.FindByID(ctx, e.ArticleID); err == nil {
			title, status = a.Title, string(a.Status)
		}
		r := Row{
			OccurredAt: e.OccurredAt, ArticleID: e.ArticleID, Title: title, Status: status,
			Detail: e.ReasonDetail, Score: e.Score, Threshold: e.Threshold, BelowThreshold: e.Score < e.Threshold,
		}
		if r.BelowThreshold {
			below++
		}
		rows = append(rows, r)
	}
	return rows, below, nil
}
