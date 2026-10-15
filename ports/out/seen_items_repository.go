package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
)

// SeenClaimStatus is the outcome of one SeenItemsRepository.Claim call.
type SeenClaimStatus string

const (
	// SeenClaimNew means no row existed for this identity (or a stale
	// in-flight row was reclaimed): the caller should run the full
	// pipeline using the returned ArticleID.
	SeenClaimNew SeenClaimStatus = "new"
	// SeenClaimSeen means a terminal outcome is already recorded and the
	// content hash is unchanged: the caller must skip the pipeline
	// entirely.
	SeenClaimSeen SeenClaimStatus = "seen"
	// SeenClaimRevised means a terminal outcome is recorded but the
	// content hash differs: whether to reprocess is governed by
	// article.IsReprocessable(TerminalOutcome) — see the design doc §5.
	SeenClaimRevised SeenClaimStatus = "revised"
	// SeenClaimInFlight means another (not yet finalized, not stale)
	// worker already claimed this identity: the caller should skip this
	// poll and retry on the next one.
	SeenClaimInFlight SeenClaimStatus = "in_flight"
)

// SeenClaim is the result of one Claim call.
type SeenClaim struct {
	Status               SeenClaimStatus
	ArticleID            string
	TerminalOutcome      string
	PreviousContentHash  string
	PreviousCanonicalURL string
	SeenCount            int
	Revision             int
	FirstSeenAt          time.Time
}

// SeenItemsRepository is the stable-ingestion-identity registry (issue
// #154): one row per (platform, source, identity key), recording whether
// an item has already been assigned a terminal outcome so ProcessCycle
// never re-runs the dictionary matcher, classifier, dedup cascade,
// clustering, or publish for something it has already decided.
type SeenItemsRepository interface {
	// Claim atomically records a new pending row for identity if none
	// exists yet, or reports the existing row's state. articleID is used
	// only when this turns out to be a SeenClaimNew (including a reclaimed
	// stale in-flight row, which reuses its original article_id instead of
	// articleID). reclaimAfter bounds how long an unfinalized claim is
	// honored as in-flight before a later call is allowed to reclaim it
	// (crash recovery).
	Claim(ctx context.Context, identity ingestidentity.Identity, articleID, contentHash, canonicalURL string, now time.Time, reclaimAfter time.Duration) (SeenClaim, error)
	// Finalize records identity's terminal outcome, refreshes
	// last_seen_at/canonical_url, and bumps revision whenever contentHash
	// differs from the row's previously recorded hash.
	Finalize(ctx context.Context, identity ingestidentity.Identity, outcome, contentHash, canonicalURL string, now time.Time) error
}
