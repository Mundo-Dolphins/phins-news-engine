// Package humanlabels holds the human-label operations shared by the
// `label` CLI commands and the admin label actions: setting (or relabeling)
// an article's label, resolving a legacy conflict and reading history.
package humanlabels

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
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
	ListEditorialExamples(ctx context.Context) ([]training.EditorialExample, error)
	ImportEditorialExample(ctx context.Context, e training.EditorialExample, override bool) (training.ImportOutcome, error)
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

// backfillStatuses: only rejected_quality is a direct editorial-quality
// outcome. The other terminal statuses, such as rejected_not_related and
// retained_no_publish, occur upstream in relevance/clustering, so those
// articles never reached the editorial classifier and cannot teach its
// publish/reject boundary.
var backfillStatuses = []article.Status{article.StatusRejectedQuality}

// UnlabeledArticles lists articles in the given statuses that have no label
// yet. It is satisfied by *sqlite.ArticleRepository.
type UnlabeledArticles interface {
	ListByStatusUnlabeled(ctx context.Context, statuses []article.Status, limit int) ([]article.Article, error)
}

// BackfillReport is what BackfillRejects found and did.
type BackfillReport struct {
	// Candidates is how many unlabeled rejected articles were found.
	Candidates int
	// Labeled is how many were labeled reject.
	Labeled int
	// Skipped is how many were already labeled by the time they were reached.
	Skipped int
}

// BackfillRejects labels real pipeline-rejected articles (up to limit) as
// reject training examples. With dryRun it only counts candidates.
func BackfillRejects(ctx context.Context, articles UnlabeledArticles, labels Repo, limit int, dryRun bool, now time.Time) (BackfillReport, error) {
	candidates, err := articles.ListByStatusUnlabeled(ctx, backfillStatuses, limit)
	if err != nil {
		return BackfillReport{}, fmt.Errorf("train backfill-rejects: %w", err)
	}
	rep := BackfillReport{Candidates: len(candidates)}
	for _, a := range candidates {
		if dryRun {
			continue
		}
		example := training.Example{
			ArticleID: a.ID, Label: "reject", LabeledAt: now,
			LabeledBy: "pipeline_backfill", Reason: string(a.Status), Source: "pipeline_backfill",
		}
		if err := labels.Label(ctx, example); err != nil {
			if errors.Is(err, training.ErrAlreadyLabeled) {
				rep.Skipped++
				continue
			}
			return rep, fmt.Errorf("train backfill-rejects: label %s: %w", a.ID, err)
		}
		rep.Labeled++
	}
	return rep, nil
}

// ImportTally counts what an import did with each snapshot.
type ImportTally struct {
	// Inserted snapshots became the article's active label.
	Inserted int
	// Unchanged snapshots already agreed with the active label.
	Unchanged int
	// Conflicts disagreed with the active label and were left as-is.
	Conflicts int
	// Superseded snapshots replaced a disagreeing active label.
	Superseded int
}

func (t *ImportTally) add(o training.ImportOutcome) {
	switch o {
	case training.ImportInserted:
		t.Inserted++
	case training.ImportUnchanged:
		t.Unchanged++
	case training.ImportConflict:
		t.Conflicts++
	case training.ImportSuperseded:
		t.Superseded++
	}
}

func (t ImportTally) String() string {
	return fmt.Sprintf("%d inserted, %d unchanged, %d superseded, %d conflict(s) left as-is", t.Inserted, t.Unchanged, t.Superseded, t.Conflicts)
}

// ArchiveReadError is returned by ImportFromDB when the archive's labels
// cannot be read; nothing was imported.
type ArchiveReadError struct{ Err error }

func (e *ArchiveReadError) Error() string { return e.Err.Error() }
func (e *ArchiveReadError) Unwrap() error { return e.Err }

// ImportFromDB copies the archive's active labels into repo. It is
// additive: it never overrides an existing label.
func ImportFromDB(ctx context.Context, repo, archive Repo) (ImportTally, error) {
	examples, err := archive.ListEditorialExamples(ctx)
	if err != nil {
		return ImportTally{}, &ArchiveReadError{Err: err}
	}
	var tally ImportTally
	for _, e := range examples {
		outcome, err := repo.ImportEditorialExample(ctx, e, false)
		if err != nil {
			return tally, err
		}
		tally.add(outcome)
	}
	return tally, nil
}

// ImportFromDataset imports a reviewed dataset's labels into repo. Only rows
// whose label source is in override may supersede a disagreeing active label.
func ImportFromDataset(ctx context.Context, repo Repo, ds editorialtrain.Dataset, override map[string]bool) (ImportTally, error) {
	var tally ImportTally
	for _, ex := range ds.Examples {
		outcome, err := repo.ImportEditorialExample(ctx, EditorialExampleFrom(ex), override[ex.LabelSource])
		if err != nil {
			return tally, err
		}
		tally.add(outcome)
	}
	return tally, nil
}

// EditorialExampleFrom maps a dataset row back to the durable snapshot
// shape ExportDataset built it from.
func EditorialExampleFrom(ex editorialtrain.TrainingExample) training.EditorialExample {
	return training.EditorialExample{
		ArticleID: ex.ArticleID, Label: ex.Label, Reason: ex.Reason, LabeledAt: ex.LabeledAt,
		Revision: ex.Revision, ContentHash: ex.ContentHash, Source: ex.LabelSource,
		Title: ex.Title, Body: ex.Body, SourceID: ex.SourceID, SourceType: ex.SourceType, Domain: ex.Domain,
		ContentType: ex.ContentType, PublishedAt: ex.PublishedAt, FetchedAt: ex.FetchedAt, ClusterID: ex.ClusterID,
	}
}
