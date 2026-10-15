-- migrations/0028_source_policy_cooldowns.sql
--
-- source_policy_cooldowns is the reservation layer for
-- sourcepolicy.Policy.EventCooldown: one row per
-- (source_id, cluster_id) — PRIMARY KEY — acquired via the same
-- single-statement CAS UPSERT pattern migration 0018 established for
-- event_cluster_publications:
--
--   INSERT INTO source_policy_cooldowns (...) VALUES (...)
--   ON CONFLICT(source_id, cluster_id) DO UPDATE SET ...
--   WHERE state = 'released' OR cooldown_until <= excluded.reserved_at
--
-- which inserts (no row existed), updates (the existing row is
-- 'released' OR its cooldown_until has already naturally elapsed), or
-- changes nothing (RowsAffected = 0: an active window is genuinely
-- held) — no error on a lost CAS, exactly like 0018. The
-- "cooldown_until <= excluded.reserved_at" branch is both the natural
-- re-acquisition path once a window expires AND the crash/lease-expiry
-- backstop: no (source_id, cluster_id) pair can be blocked past its own
-- configured EventCooldown even if Release is never called.
--
-- State transitions: 'reserved' (Gate B reserved this window, inside
-- the same transaction as the rest of processOne) -> 'confirmed'
-- (PublishOutbox delivered the held article to Telegram) or 'released'
-- (an editorial/freshness/cluster-gate rejection, or a terminal outbox
-- failure, happened after the reservation was acquired but before
-- delivery). cooldown_until is fixed at reserved_at + EventCooldown and
-- is never moved by Confirm — the window's duration is anchored to
-- Gate B's decision time, not to Telegram's eventual delivery time. A
-- MemberRoleCorrection publish always calls ForceReserve instead of
-- Reserve (unconditional overwrite, no CAS condition) since corrections
-- bypass the cooldown check entirely by design but must still refresh
-- the window for a later confirmation of the same event.
CREATE TABLE source_policy_cooldowns (
    source_id      TEXT NOT NULL,
    cluster_id     TEXT NOT NULL,
    state          TEXT NOT NULL CHECK (state IN ('reserved','confirmed','released')),
    article_id     TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    decision_code  TEXT NOT NULL DEFAULT '',
    policy_version TEXT NOT NULL DEFAULT '',
    outbox_id      INTEGER REFERENCES publication_outbox(id) ON DELETE SET NULL,
    reserved_at    TIMESTAMP NOT NULL,
    confirmed_at   TIMESTAMP,
    released_at    TIMESTAMP,
    cooldown_until TIMESTAMP NOT NULL,
    PRIMARY KEY (source_id, cluster_id)
);
CREATE INDEX idx_source_policy_cooldowns_article ON source_policy_cooldowns (article_id);

-- Backfill so an upgraded deployment starts consistent, from real ground
-- truth (publication_attempts.outcome = 'succeeded'), never from the
-- flawed would_publish_special history this migration's application-code
-- counterpart fixes. Every (source_id, cluster_id) pair with a
-- successfully published confirmation/substantial-update gets a
-- 'confirmed' row for the LATEST such article. cooldown_until is set to
-- the backfilled row's own timestamp (already expired at migration-apply
-- time) rather than a reconstructed future window, because the original
-- per-source EventCooldown duration is not recoverable from historical
-- data alone — this is a deliberate, one-time, non-retroactive choice
-- (see the design doc's Consequences section), matching 0018's own
-- backfill philosophy of starting consistent going forward, not
-- rewriting history.
INSERT INTO source_policy_cooldowns (
    source_id, cluster_id, state, article_id, decision_code, policy_version,
    outbox_id, reserved_at, confirmed_at, cooldown_until
)
SELECT
    ranked.source_id, ranked.cluster_id, 'confirmed', ranked.article_id,
    ranked.decision_code, ranked.policy_version, ranked.outbox_id,
    ranked.attempted_at, ranked.attempted_at, ranked.attempted_at
FROM (
    SELECT
        spd.source_id AS source_id, spd.cluster_id AS cluster_id, spd.article_id AS article_id,
        spd.decision_code AS decision_code, spd.policy_version AS policy_version,
        po.id AS outbox_id, pa.attempted_at AS attempted_at,
        ROW_NUMBER() OVER (PARTITION BY spd.source_id, spd.cluster_id ORDER BY pa.attempted_at DESC) AS rn
    FROM source_policy_decisions spd
    JOIN publication_outbox po ON po.article_id = spd.article_id
    JOIN publication_attempts pa ON pa.outbox_id = po.id AND pa.outcome = 'succeeded'
    WHERE spd.decision_code IN ('publish_official_confirmation', 'publish_substantial_update')
      AND spd.cluster_id IS NOT NULL AND spd.cluster_id != ''
      AND po.status = 'delivered'
) ranked
WHERE ranked.rn = 1;

-- In-flight (not yet resolved to a terminal outcome) confirmation/
-- substantial-update items get a 'reserved' row instead, for the same
-- reason and with the same non-retroactive cooldown_until choice, only
-- where no 'confirmed' row was already backfilled above for that
-- (source_id, cluster_id) pair. "In-flight" is every publication_outbox
-- status except the two terminal ones ('delivered', already handled
-- above, and 'dead', a permanently failed/exhausted item that must NOT
-- hold a cooldown slot) — 'pending', 'leased', 'sent_unconfirmed',
-- 'retryable', and 'quarantined' all still represent an unresolved
-- attempt that could yet confirm, so all five stay reserved. See
-- migration 0023_outbox_delivery_guarantee.sql for this status
-- vocabulary (it renamed 'claimed'->'leased', 'published'->'delivered',
-- and added 'sent_unconfirmed'/'retryable'/'quarantined' — the pre-0023
-- vocabulary this migration's application-code counterpart's design
-- phase mistakenly assumed does not exist by the time this migration
-- runs, since 0028 applies after 0023 in every fresh or upgraded
-- database).
INSERT INTO source_policy_cooldowns (
    source_id, cluster_id, state, article_id, decision_code, policy_version,
    outbox_id, reserved_at, cooldown_until
)
SELECT
    ranked.source_id, ranked.cluster_id, 'reserved', ranked.article_id,
    ranked.decision_code, ranked.policy_version, ranked.outbox_id, ranked.created_at, ranked.created_at
FROM (
    SELECT
        spd.source_id AS source_id, spd.cluster_id AS cluster_id, spd.article_id AS article_id,
        spd.decision_code AS decision_code, spd.policy_version AS policy_version,
        po.id AS outbox_id, po.created_at AS created_at,
        ROW_NUMBER() OVER (PARTITION BY spd.source_id, spd.cluster_id ORDER BY po.created_at DESC) AS rn
    FROM source_policy_decisions spd
    JOIN publication_outbox po ON po.article_id = spd.article_id
    WHERE spd.decision_code IN ('publish_official_confirmation', 'publish_substantial_update')
      AND spd.cluster_id IS NOT NULL AND spd.cluster_id != ''
      AND po.status NOT IN ('delivered', 'dead')
      AND (spd.source_id, spd.cluster_id) NOT IN (SELECT source_id, cluster_id FROM source_policy_cooldowns)
) ranked
WHERE ranked.rn = 1;
