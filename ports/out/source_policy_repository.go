package out

import (
	"context"
	"time"
)

// SourcePolicyDecisionRecord is one persisted evaluation of Phase 12's
// source-policy engine, audit-trail shaped to mirror
// event_clustering_decisions. DecisionCode/MemberRole/Novelty/DuplicateType
// are stored as plain strings (their domain types' underlying type is
// already string) so this port has no dependency on
// domain/sourcepolicy — matching this codebase's existing
// out-port convention of using primitive/DTO shapes, not domain types
// directly, at the persistence boundary.
type SourcePolicyDecisionRecord struct {
	ID                   string
	ArticleID            string
	SourceID             string
	Authority            string
	PolicyVersion        string
	ClusterID            string // "" when the article has no cluster yet
	DedupMatchType       string
	MemberRole           string
	Novelty              string
	DecisionCode         string
	WouldPublishStandard bool
	WouldPublishSpecial  bool
	Mode                 string
	// CooldownOutcome is sourcepolicy.ReservationOutcome as a plain
	// string — what the cooldown reservation layer
	// (SourcePolicyCooldownRepository) actually did with this
	// decision's ShouldPublish verdict: "" (not applicable),
	// "shadow" (mode != enforce, no attempt made), "reserved",
	// "forced" (MemberRoleCorrection), or "blocked_by_cooldown" (lost
	// the CAS race). WouldPublishSpecial above stays pure Decide
	// telemetry, unaffected by this field — see the design doc.
	CooldownOutcome string
	Explanation     string
	DecidedAt       time.Time
}

// SourcePolicyDecisionRepository persists and queries source-policy audit
// records.
type SourcePolicyDecisionRepository interface {
	// Save persists one record. ID is caller-supplied (uuid), matching
	// this codebase's other append-only audit tables.
	Save(ctx context.Context, r SourcePolicyDecisionRecord) error

	// ByArticle returns every record for one article, oldest first —
	// used by `source-policy simulate --article-id`.
	ByArticle(ctx context.Context, articleID string) ([]SourcePolicyDecisionRecord, error)

	// ListByRange returns every record with DecidedAt in [from, to),
	// optionally filtered by sourceID and mode (either may be "" to skip
	// that filter) — used by `source-policy audit`.
	ListByRange(ctx context.Context, from, to time.Time, sourceID, mode string) ([]SourcePolicyDecisionRecord, error)
}
