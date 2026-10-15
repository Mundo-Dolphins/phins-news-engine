-- Separate normal editorial traffic from structured live milestones. Legacy
-- rows are explicitly normal so old configurations and rows keep their policy.
ALTER TABLE publication_outbox ADD COLUMN lane TEXT NOT NULL DEFAULT 'normal'
    CHECK (lane IN ('normal','live'));
CREATE INDEX idx_outbox_lane_status_available
    ON publication_outbox (lane, status, available_at, expires_at);
