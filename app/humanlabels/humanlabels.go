// Package humanlabels holds the human-label operations shared by the
// `label` CLI commands and the admin label actions: setting (or relabeling)
// an article's label, resolving a legacy conflict and reading history.
package humanlabels

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// ErrUnresolvedConflict is returned (wrapped) by Set when the article has an
// unresolved legacy label conflict and must go through ResolveConflict.
var ErrUnresolvedConflict = errors.New("unresolved legacy label conflict")

// conflictError carries the operator-facing text and matches
// ErrUnresolvedConflict through errors.Is.
type conflictError struct{ articleID, label string }

func (e *conflictError) Error() string {
	return fmt.Sprintf("label: %s has an unresolved legacy label conflict -- use \"label resolve-conflict %s %s\" instead of \"label set\"", e.articleID, e.articleID, e.label)
}

func (e *conflictError) Unwrap() error { return ErrUnresolvedConflict }

// Repo is the human-label storage the operations need. It is satisfied by
// *sqlite.HumanLabelRepository.
type Repo interface {
	GetActive(ctx context.Context, articleID string) (training.Example, bool, error)
	Relabel(ctx context.Context, e training.Example, expectedRevision int) error
	Label(ctx context.Context, e training.Example) error
	ListUnresolvedConflicts(ctx context.Context) ([]training.Conflict, error)
	ListByArticle(ctx context.Context, articleID string) ([]training.Example, error)
	ResolveConflict(ctx context.Context, articleID string, e training.Example) error
}

// HardExamples closes the hard-example mining loop once a human has labeled
// an article.
type HardExamples interface {
	// MarkResolved marks the article's hard-example candidate resolved; it is
	// a no-op for an article with no candidate.
	MarkResolved(ctx context.Context, articleID string, at time.Time) error
}

// SetOutcome describes what Set did.
type SetOutcome struct {
	// Relabeled is true when an active label was superseded.
	Relabeled bool
	// Previous is the superseded label (only when Relabeled).
	Previous string
	// Revision is the resulting revision when Relabeled.
	Revision int
}

// HistoryEntry is one label revision of an article.
type HistoryEntry struct {
	training.Example
	// Active is true for the article's current active revision.
	Active bool
}

// markResolved is best-effort: most labeled articles were never mined, and a
// failure must not fail a label the repository already accepted.
func markResolved(ctx context.Context, hard HardExamples, articleID string) error {
	if err := hard.MarkResolved(ctx, articleID, time.Now().UTC()); err != nil {
		return fmt.Errorf("failed to mark hard-example candidate for %s resolved: %w", articleID, err)
	}
	return nil
}

// Set labels an article, relabeling when it already has an active label. It
// refuses an article with an unresolved legacy conflict (ErrUnresolvedConflict).
// warning is the best-effort hard-example MarkResolved failure.
func Set(ctx context.Context, labels Repo, hard HardExamples, ex training.Example) (outcome SetOutcome, warning error, err error) {
	active, hasActive, err := labels.GetActive(ctx, ex.ArticleID)
	if err != nil {
		return SetOutcome{}, nil, fmt.Errorf("label: check existing label for %s: %w", ex.ArticleID, err)
	}
	if hasActive {
		if err := labels.Relabel(ctx, ex, active.Revision); err != nil {
			return SetOutcome{}, nil, fmt.Errorf("label: relabel %s: %w", ex.ArticleID, err)
		}
		return SetOutcome{Relabeled: true, Previous: active.Label, Revision: active.Revision + 1}, markResolved(ctx, hard, ex.ArticleID), nil
	}
	// No active label can also mean an unresolved legacy conflict (migration
	// 0026 leaves every row of an ambiguous group inactive). Labeling through
	// would orphan the conflict row, so send the operator to ResolveConflict,
	// which does both halves atomically.
	conflicts, err := labels.ListUnresolvedConflicts(ctx)
	if err != nil {
		return SetOutcome{}, nil, fmt.Errorf("label: check unresolved conflicts for %s: %w", ex.ArticleID, err)
	}
	for _, c := range conflicts {
		if c.ArticleID == ex.ArticleID {
			return SetOutcome{}, nil, &conflictError{articleID: ex.ArticleID, label: ex.Label}
		}
	}
	if err := labels.Label(ctx, ex); err != nil {
		return SetOutcome{}, nil, err
	}
	return SetOutcome{}, markResolved(ctx, hard, ex.ArticleID), nil
}

// ResolveConflict resolves an article's legacy label conflict by choosing the
// winning label. warning is the best-effort hard-example failure.
func ResolveConflict(ctx context.Context, labels Repo, hard HardExamples, ex training.Example) (warning error, err error) {
	if err := labels.ResolveConflict(ctx, ex.ArticleID, ex); err != nil {
		return nil, err
	}
	return markResolved(ctx, hard, ex.ArticleID), nil
}

// History returns every label revision of an article, active and
// superseded, in the repository's order.
func History(ctx context.Context, labels Repo, articleID string) ([]HistoryEntry, error) {
	rows, err := labels.ListByArticle(ctx, articleID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	active, hasActive, err := labels.GetActive(ctx, articleID)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(rows))
	for _, e := range rows {
		out = append(out, HistoryEntry{Example: e, Active: hasActive && active.Revision == e.Revision})
	}
	return out, nil
}
