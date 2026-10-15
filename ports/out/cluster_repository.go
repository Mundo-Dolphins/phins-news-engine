package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// ClusterLifecycleStats summarizes event_clusters' current lifecycle
// state, computed fresh on every call. There is no metrics/Prometheus backend in this
// codebase today; this struct exists to be logged as structured fields,
// the same convention RunRetention/RetentionResult already established.
type ClusterLifecycleStats struct {
	OpenCount          int
	ClosedCount        int
	OpenAgeP95Seconds  float64
	OpenAgeMaxSeconds  float64
	OpenMemberCountMax int
	// ReopenedTotal is the lifetime SUM of every cluster's reopened_count
	// — a running total, not a per-run delta.
	ReopenedTotal int
}

// ClusterRepository groups articles believed to describe the same event,
// and persists the event-clustering domain's inputs (representations, event
// keys) and outputs (decisions, representative selection) for audit and for
// bounding future candidate lookups. It is one interface — rather than
// several smaller ones — because most methods here are written/read by one
// caller (the clustering step of ProcessCycle, Task 8) around the same
// per-article unit of work, and the lifecycle-maintenance methods
// (CloseInactiveBatch, Stats — called by CloseInactiveClusters, not
// ProcessCycle) operate on the same underlying rows and storage concerns;
// splitting either group into a second interface would not change who
// implements it, only add an extra type name to thread through call sites.
type ClusterRepository interface {
	// CreateCluster creates a new cluster with the given id, a single
	// founding member (articleID, persisted with role
	// clustering.MemberRoleFirstReport per that constant's own doc comment
	// — DecideJoin never assigns it, the ActionCreate caller does), and
	// status, and points articles.current_cluster_id at it.
	CreateCluster(ctx context.Context, id, articleID string, status clustering.Status, now time.Time) error
	// AddMember adds articleID to an existing cluster with the given role
	// and clustering score (clustering.Comparison.Score for the comparison
	// that won the join), bumping the cluster's member count and
	// last_updated_at, and points articles.current_cluster_id at it. Full
	// join evidence for articleID is expected to already be persisted
	// separately via SaveDecision — AddMember itself stores only role and
	// score, not evidence, to avoid keeping two copies in sync (see
	// migration 0008's doc comment on event_cluster_members).
	//
	// If clusterID is currently closed, AddMember unconditionally reopens
	// it (status -> 'open', closed_at -> NULL, reopened_count += 1) rather
	// than rejecting the join or requiring a separate reopen call. This
	// preserves the cluster's existing event_cluster_publications gate
	// row — the whole reason a late-but-legitimate join must land on the
	// *same* cluster_id rather than a new one. close_reason is
	// deliberately left as-is on reopen (not cleared): it reads as "the
	// reason for the most recent close," which is intentional, not an
	// oversight.
	AddMember(ctx context.Context, clusterID, articleID string, role clustering.MemberRole, score float64, now time.Time) error
	// DeleteClosedOlderThan removes closed clusters (and their members,
	// event keys, clustering decisions, representative history, and
	// publication-gate row, all via ON DELETE CASCADE) whose closed_at is
	// older than cutoff — not last_updated_at, which on a closed cluster
	// still reflects its last member's join time rather than when it
	// became eligible for deletion. Any article still pointing at a
	// deleted cluster has its current_cluster_id cleared automatically
	// (ON DELETE SET NULL); published articles and publication_attempts
	// rows are never touched (no foreign key from either to
	// event_clusters).
	DeleteClosedOlderThan(ctx context.Context, cutoff time.Time) (int, error)

	// CloseInactiveBatch closes up to batchSize open clusters whose
	// last_updated_at is at least closeAfter old relative to now — i.e.
	// last_updated_at <= cutoff, where cutoff = now.Add(-closeAfter) is
	// computed by the caller (matching clusterlifecycle.Eligible's
	// inclusive boundary exactly). Implemented as a single conditional
	// UPDATE: the WHERE clause (status = 'open' AND last_updated_at <=
	// cutoff) is evaluated by SQLite against each row's current state at
	// execution time, not against a value read earlier by Go code — this
	// is the compare-and-swap that makes it safe to call concurrently
	// with AddMember without any separate locking or version column. closeReason is
	// stored verbatim on every closed row's close_reason column. Returns
	// the number of clusters actually closed, which is less than
	// batchSize exactly when fewer rows were eligible.
	CloseInactiveBatch(ctx context.Context, cutoff time.Time, closeReason string, batchSize int, now time.Time) (int, error)

	// Stats computes ClusterLifecycleStats as of now, for per-run
	// observability logging (see CloseInactiveClusters.Run).
	Stats(ctx context.Context, now time.Time) (ClusterLifecycleStats, error)

	// SaveRepresentation persists rep — the eventsignals.Extract output for
	// one article — for later candidate scoring and audit. rulesVersion is
	// supplied by the caller because eventsignals.Representation itself
	// carries no such field (only eventsignals.Rules.Version does). Must be
	// called after the article row already exists (rep.ArticleID
	// references articles(id)). Safe to call more than once for the same
	// ArticleID: a later call replaces the earlier row.
	SaveRepresentation(ctx context.Context, rep eventsignals.Representation, rulesVersion string, now time.Time) error
	// SaveEventKeys persists keys (rep.ApproximateKeys, strongest-to-weakest
	// ordered) against articleID and the cluster it was decided to belong
	// to (clusterID — the newly created cluster for ActionCreate, or the
	// joined cluster for ActionJoin). Takes clusterID as a separate
	// argument, rather than requiring SaveRepresentation to know it, because
	// a representation can be (and typically is) extracted and scored
	// against candidates before the clustering decision — and therefore the
	// owning cluster — exists. Safe to call more than once for the same
	// (articleID, key) pair; duplicates are ignored.
	SaveEventKeys(ctx context.Context, articleID, clusterID string, keys []string, now time.Time) error
	// FindCandidatesByEventKeys returns, for each of keys, every other
	// article's persisted event key row within windowStart (inclusive, by
	// articles.published_at) — excluding excludeArticleID — as
	// clustering.Candidate values ready for clustering.Compare, ordered by
	// key strength (strongest match first) then most-recently-published,
	// capped at limit. This is the sole candidate-generation path: it never
	// scans articles or event_representations beyond what the event_key
	// index and the time-window/limit bound, per the plan's "do not compare
	// each incoming article against the whole article table" constraint.
	// Each returned Candidate's DuplicateRelation/DuplicateScore are left
	// zero — this repository has no opinion on deduplication; a caller that
	// wants those populated fills them in from deduplication.Decide
	// separately (ADR 0009).
	FindCandidatesByEventKeys(ctx context.Context, keys []string, windowStart time.Time, excludeArticleID string, limit int) ([]clustering.Candidate, error)
	// SaveDecision persists d — the outcome of clustering.DecideJoin for
	// articleID — for audit. chosenClusterID is the cluster articleID
	// actually ended up in (the newly created cluster for ActionCreate, the
	// joined cluster for ActionJoin, or "" for ActionAmbiguous before a
	// caller creates a fallback cluster) — supplied separately from
	// d.ClusterID because Decision.ClusterID is, by the domain package's own
	// contract, only ever set for ActionJoin.
	SaveDecision(ctx context.Context, articleID, chosenClusterID string, d clustering.Decision) error

	// UpdateRepresentative records that articleID is now clusterID's
	// representative, per score/evidence/rulesVersion (a
	// clustering.RepresentativeScore already computed by a caller via
	// clustering.ReplacesCurrent — this method does not decide anything,
	// only writes). The prior representative_article_id/score/evidence/
	// rules_version on event_clusters (if any) is archived as a row in the
	// representative-switch history table before being overwritten, so a
	// switch is auditable rather than silently overwriting the prior value.
	// A caller is expected to invoke this only when
	// clustering.ReplacesCurrent already returned true — UpdateRepresentative
	// itself applies unconditionally.
	//
	// Must not be called from inside an outer UnitOfWork.Do(...) closure:
	// it opens its own transaction to read-then-archive-then-write
	// atomically, and under this project's single-connection pool
	// (SetMaxOpenConns(1)) nesting transactions would deadlock rather than
	// error.
	UpdateRepresentative(ctx context.Context, clusterID, articleID string, score float64, evidence []clustering.Evidence, rulesVersion string, now time.Time) error
}
