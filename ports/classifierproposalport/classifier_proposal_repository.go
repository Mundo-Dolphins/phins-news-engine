// Package classifierproposalport declares the driven port for offline,
// review-gated classifier dictionary proposals.
package classifierproposalport

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
)

type UnknownOccurrenceAggregate struct {
	Value        string
	Normalized   string
	Occurrences  int
	ArticleID    string
	ArticleTitle string
	FirstSeen    time.Time
	LastSeen     time.Time
}

type Metrics struct {
	AnalysisRunsTotal            int
	ProposalsTotal               int
	ProposalsApprovedTotal       int
	ProposalsRejectedTotal       int
	UnknownEntityCount           int
	EntityExtractionTotal        int
	EntityExtractionUnknownTotal int
}

type ClassifierProposalRepository interface {
	Upsert(ctx context.Context, p classifierproposal.Proposal) (string, error)
	Get(ctx context.Context, id string) (classifierproposal.Proposal, error)
	List(ctx context.Context, kind, status string) ([]classifierproposal.Proposal, error)
	UpdateStatus(ctx context.Context, id string, status classifierproposal.Status, decidedAt time.Time) error
	FilterUnprocessed(ctx context.Context, analyzerVersion string, articleIDs []string) ([]string, error)
	MarkArticlesProcessed(ctx context.Context, analyzerVersion string, articleIDs []string, processedAt time.Time) error
	ListUnknownAggregates(ctx context.Context, since time.Time, minOccurrences, limit int) ([]UnknownOccurrenceAggregate, error)
	Metrics(ctx context.Context, since time.Time) (Metrics, error)
}
