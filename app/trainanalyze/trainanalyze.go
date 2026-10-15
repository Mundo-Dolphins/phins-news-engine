// Package trainanalyze computes `train analyze`'s read-only diagnostic
// counts: how many LLM
// proposals are pending, how many of those clear the auto-label
// threshold (editoriallabelproposal.ComputeTier's Silver-vs-Pending
// split), and how the current review queue breaks down by its
// dominant priority-score component (reviewpriority.buildReason's
// first, highest-contribution term).
package trainanalyze

import (
	"context"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

// Result is Analyze's output.
type Result struct {
	ProposedCount             int
	AutoLabeledCount          int
	SentToReviewCount         int
	QueueSize                 int
	QueueDisagreementCount    int
	QueueNovelVocabularyCount int
	QueueOtherCount           int
	// ManualReviewRate is the share of analysed proposals that fall
	// below the auto-label threshold and so need a human: SentToReviewCount / ProposedCount, 0 when nothing was
	// analysed.
	ManualReviewRate float64
}

// Analyze gathers the diagnostic counts `train analyze` reports and
// `train run`'s textual report includes. queueLimit bounds how many
// queue entries ListOrderedByScore returns (0 or negative means "no
// limit", per reviewqueueport's convention — pass a large value like
// 100000 from callers that want the true total).
func Analyze(ctx context.Context, proposals editoriallabelproposalport.EditorialLabelProposalRepository, queue reviewqueueport.ReviewQueueRepository, autoLabelThreshold float64, queueLimit int) (Result, error) {
	pending, err := proposals.ListByStatus(ctx, editoriallabelproposal.StatusPending)
	if err != nil {
		return Result{}, err
	}
	var autoLabeled, sentToReview int
	for _, p := range pending {
		if p.Confidence >= autoLabelThreshold {
			autoLabeled++
		} else {
			sentToReview++
		}
	}

	entries, err := queue.ListOrderedByScore(ctx, queueLimit)
	if err != nil {
		return Result{}, err
	}
	var disagreement, novelVocab, other int
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.Reason, "disagreement="):
			disagreement++
		case strings.HasPrefix(e.Reason, "novel_vocabulary="):
			novelVocab++
		default:
			other++
		}
	}

	var reviewRate float64
	if len(pending) > 0 {
		reviewRate = float64(sentToReview) / float64(len(pending))
	}

	return Result{
		ManualReviewRate: reviewRate,
		ProposedCount:    len(pending), AutoLabeledCount: autoLabeled, SentToReviewCount: sentToReview,
		QueueSize: len(entries), QueueDisagreementCount: disagreement,
		QueueNovelVocabularyCount: novelVocab, QueueOtherCount: other,
	}, nil
}
