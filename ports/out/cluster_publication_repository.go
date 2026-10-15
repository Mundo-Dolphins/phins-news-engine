package out

import (
	"context"
	"time"
)

// ClusterPublicationReservation is one cluster's claim on its
// initial-publication slot — see migration
// 0018_cluster_publication_gate.sql. Fields are plain strings
// (MemberRole/DecisionCode), not domain/clustering or
// domain/sourcepolicy types, matching this codebase's existing
// out-port convention — see SourcePolicyDecisionRecord for the established
// precedent.
type ClusterPublicationReservation struct {
	ClusterID     string
	ArticleID     string
	SourceID      string
	MemberRole    string
	DecisionCode  string
	PolicyVersion string
	Reason        string
}

// ClusterPublicationRepository persists the cluster publication gate: the
// compare-and-swap reservation that guarantees at most one initial
// publication per event_clusters row, independent of sourcepolicy.Mode.
type ClusterPublicationRepository interface {
	// Reserve attempts to atomically claim r.ClusterID's initial-publication
	// slot for r.ArticleID. Succeeds (acquired=true) when no row exists yet
	// for this cluster, or the existing row is in the 'released' state;
	// fails (acquired=false, err=nil — an expected outcome, not an error)
	// when the existing row is 'reserved' or 'confirmed' by a different
	// claim. Must be called inside the same ports/out.UnitOfWork.Do(...)
	// transaction as the outbox Enqueue it is paired with, so a later
	// failure in that same transaction rolls the reservation back too.
	Reserve(ctx context.Context, r ClusterPublicationReservation, now time.Time) (acquired bool, err error)
	// Confirm marks clusterID's reservation confirmed and links outboxID,
	// when the outbox item it was reserved for is actually published.
	// Scoped to (clusterID, articleID): a no-op if articleID does not
	// currently hold the reservation (e.g. a later material-update
	// publication for the same cluster, which never reserved it).
	Confirm(ctx context.Context, clusterID, articleID string, outboxID int64, now time.Time) error
	// Release marks clusterID's reservation released when its outbox item
	// has gone dead (retry budget exhausted or a permanent error) without
	// ever publishing, freeing the cluster for a future article to claim
	// the slot. Scoped to (clusterID, articleID) exactly like Confirm.
	Release(ctx context.Context, clusterID, articleID string, now time.Time) error
}

// ClusterPublishedSourceReader is an optional capability of a
// ClusterPublicationRepository: it lists the sources that already have an
// article of this cluster queued or published (excluding one article), so the
// cluster gate can tell an official cross-post of already-official content
// from a genuine first official confirmation.
type ClusterPublishedSourceReader interface {
	PublishedSourceIDs(ctx context.Context, clusterID, excludeArticleID string) ([]string, error)
}
