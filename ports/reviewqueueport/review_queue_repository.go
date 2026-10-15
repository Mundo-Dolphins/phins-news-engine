// ports/reviewqueueport/review_queue_repository.go
//
// Package reviewqueueport declares the driven ports for the
// review-priority queue: the live
// review_queue table itself, and read-only access to the evidence
// tables review-priority scoring needs (unknown entities, dictionary
// proposals).
package reviewqueueport

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
)

// ReviewQueueRepository persists the live review_queue: at most one row
// per article, upserted whenever editorial classify's sync step
// (re)scores it, and deleted once a human resolves it or a later sync
// finds it's become an obvious agreement.
type ReviewQueueRepository interface {
	Upsert(ctx context.Context, e reviewpriority.QueueEntry) error
	Delete(ctx context.Context, articleID string) error
	// ListOrderedByScore returns up to limit queue rows, highest score
	// first (ties broken by oldest created_at first).
	ListOrderedByScore(ctx context.Context, limit int) ([]reviewpriority.QueueEntry, error)
}

// ReviewSignalsRepository reads the existing evidence tables
// reviewpriority.Score's novel_vocabulary and unknown_entities
// components need.
type ReviewSignalsRepository interface {
	CountUnknownEntities(ctx context.Context, articleID string) (int, error)
	CountPendingDictionaryProposals(ctx context.Context, articleID string) (int, error)
}
