// Package trainreview holds the per-item logic of the human review of the
// LLM teacher's queued proposals, shared by the `train review` CLI and the
// admin panel: loading the queue joined with articles and label history,
// and applying one publish/discard decision.
package trainreview

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

// Decision is a reviewer's verdict on a queued article.
type Decision string

const (
	// Publish labels the article "publish" and approves its proposal.
	Publish Decision = "publish"
	// Discard labels the article "discard" and rejects its proposal.
	Discard Decision = "discard"
)

// Outcome says what Decide did with a queue entry.
type Outcome string

const (
	// Decided means the label was written, the proposal resolved and the
	// queue entry deleted.
	Decided Outcome = "decided"
	// DroppedAlreadyLabeled means the article had been labeled through
	// another path; only the queue entry was deleted.
	DroppedAlreadyLabeled Outcome = "dropped_already_labeled"
	// DroppedPurged means the article no longer exists; only the queue
	// entry was deleted.
	DroppedPurged Outcome = "dropped_purged"
)

// ArticleGetter is the read access over stored articles -- satisfied by
// *sqlite.ArticleRepository.
type ArticleGetter interface {
	FindByID(ctx context.Context, id string) (article.Article, error)
}

// LabelHistory lists every label revision of an article, oldest first --
// satisfied by ports/out.HumanLabelRepository.
type LabelHistory interface {
	ListByArticle(ctx context.Context, articleID string) ([]training.Example, error)
}

// Deps are the repositories the review logic works over.
type Deps struct {
	Articles  ArticleGetter
	Labels    appout.HumanLabelRepository
	Proposals editoriallabelproposalport.EditorialLabelProposalRepository
	Queue     reviewqueueport.ReviewQueueRepository
}

// Item is one queue entry joined with its article and label history.
// Purged is true when the article no longer exists, in which case Article
// is the zero value.
type Item struct {
	Entry       reviewpriority.QueueEntry
	Article     article.Article
	PriorLabels []training.Example
	Purged      bool
}

// Decide applies dec to the queue entry e: it writes the human label
// (with by/source), resolves the proposal (publish approves, discard
// rejects) and deletes the queue entry. An article that was purged, or
// that was already labeled elsewhere, only has its queue entry deleted and
// is reported through the Outcome. Errors carry no command prefix.
func Decide(ctx context.Context, d Deps, e reviewpriority.QueueEntry, dec Decision, by, source string, now time.Time) (Outcome, error) {
	var status editoriallabelproposal.Status
	var verb string
	switch dec {
	case Publish:
		status, verb = editoriallabelproposal.StatusApproved, "approve"
	case Discard:
		status, verb = editoriallabelproposal.StatusRejected, "reject"
	default:
		return "", fmt.Errorf("unknown decision %q", dec)
	}

	if _, err := d.Articles.FindByID(ctx, e.ArticleID); err != nil {
		if errors.Is(err, article.ErrNotFound) {
			// Purged (e.g. by retention) after the entry was queued.
			if delErr := d.Queue.Delete(ctx, e.ArticleID); delErr != nil {
				return "", fmt.Errorf("dequeue orphaned %s: %w", e.ArticleID, delErr)
			}
			return DroppedPurged, nil
		}
		return "", fmt.Errorf("find article %s: %w", e.ArticleID, err)
	}

	if err := d.Labels.Label(ctx, training.Example{
		ArticleID: e.ArticleID, Label: string(dec), LabeledAt: now, LabeledBy: by, Source: source,
	}); err != nil {
		if errors.Is(err, training.ErrAlreadyLabeled) {
			if delErr := d.Queue.Delete(ctx, e.ArticleID); delErr != nil {
				return "", fmt.Errorf("dequeue already-labeled %s: %w", e.ArticleID, delErr)
			}
			return DroppedAlreadyLabeled, nil
		}
		return "", fmt.Errorf("label %s: %w", e.ArticleID, err)
	}
	if err := d.Proposals.UpdateStatus(ctx, e.ProposalID, status, now); err != nil {
		return "", fmt.Errorf("%s proposal %s: %w", verb, e.ProposalID, err)
	}
	if err := d.Queue.Delete(ctx, e.ArticleID); err != nil {
		return "", fmt.Errorf("dequeue %s: %w", e.ArticleID, err)
	}
	return Decided, nil
}

// Queue returns up to limit queue entries, highest score first, each
// joined with its article and label history. A purged article yields an
// Item with Purged set and an empty Article.
func Queue(ctx context.Context, d Deps, labels LabelHistory, limit int) ([]Item, error) {
	entries, err := d.Queue.ListOrderedByScore(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list review queue: %w", err)
	}
	items := make([]Item, 0, len(entries))
	for _, e := range entries {
		it := Item{Entry: e}
		a, err := d.Articles.FindByID(ctx, e.ArticleID)
		switch {
		case err == nil:
			it.Article = a
		case errors.Is(err, article.ErrNotFound):
			it.Purged = true
		default:
			return nil, fmt.Errorf("find article %s: %w", e.ArticleID, err)
		}
		if !it.Purged {
			prior, err := labels.ListByArticle(ctx, e.ArticleID)
			if err != nil {
				return nil, fmt.Errorf("list labels %s: %w", e.ArticleID, err)
			}
			it.PriorLabels = prior
		}
		items = append(items, it)
	}
	return items, nil
}

// Find returns the queue entry for articleID and whether it exists. The
// queue port has no single-row getter, so it scans ListOrderedByScore.
func Find(ctx context.Context, q reviewqueueport.ReviewQueueRepository, articleID string) (reviewpriority.QueueEntry, bool, error) {
	entries, err := q.ListOrderedByScore(ctx, -1)
	if err != nil {
		return reviewpriority.QueueEntry{}, false, fmt.Errorf("list review queue: %w", err)
	}
	for _, e := range entries {
		if e.ArticleID == articleID {
			return e, true, nil
		}
	}
	return reviewpriority.QueueEntry{}, false, nil
}
