-- migrations/0022_cluster_lifecycle.sql
--
-- Gives event_clusters a real close/reopen lifecycle. Prior to this migration, status='closed' was unreachable in
-- production (only CreateCluster wrote status, and both call sites
-- hardcoded 'open'), so RunRetention.Run's DeleteClosedOlderThan call has
-- always deleted zero rows and event_clusters has grown unbounded.
--
-- closed_at anchors retention purge (RunRetention now measures "how long
-- has this cluster been closed" rather than reusing last_updated_at, which
-- on a closed cluster still reflects its last member's join time, not
-- when it became eligible for deletion). close_reason is audit-only,
-- always 'inactivity_ttl' from this codebase's own maintenance job today,
-- but kept free-text for future operator-triggered closes. reopened_count
-- is a lifetime counter incremented by AddMember whenever a late join
-- reopens an already-closed cluster — see ClusterRepository.AddMember's
-- doc comment.
ALTER TABLE event_clusters ADD COLUMN closed_at TIMESTAMP;
ALTER TABLE event_clusters ADD COLUMN close_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE event_clusters ADD COLUMN reopened_count INTEGER NOT NULL DEFAULT 0;

-- Defensive backfill: today's audit (see the design doc's Context section)
-- proves this matches zero rows in any existing deployment, since
-- status='closed' has never been reachable — kept so this migration stays
-- correct even if that ever changes before it ships.
UPDATE event_clusters SET closed_at = last_updated_at WHERE status = 'closed' AND closed_at IS NULL;

-- Speeds CloseInactiveBatch's WHERE status='open' AND last_updated_at <= ?
-- and Stats' status-partitioned aggregates.
CREATE INDEX idx_event_clusters_status_last_updated ON event_clusters (status, last_updated_at);
-- Speeds DeleteClosedOlderThan's WHERE status='closed' AND closed_at < ?.
CREATE INDEX idx_event_clusters_closed_at ON event_clusters (closed_at);
