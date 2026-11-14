// Package seenitemsbackfill backfills seen_items rows from the existing
// articles history. The seenitemsbackfill command and the seen_items.backfill
// admin action share it.
package seenitemsbackfill

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Result summarizes one Run call.
type Result struct {
	// Candidates is how many distinct (platform, source, identity) groups
	// were found in the existing articles table.
	Candidates int
	// Inserted is how many of those did not already have a seen_items row
	// and were backfilled.
	Inserted int
	// Skipped is how many already had a seen_items row (idempotent re-run).
	Skipped int
}

// Summary is the one-line report the command prints and the admin action
// returns.
func (r Result) Summary() string {
	return fmt.Sprintf("seen_items backfill: %d candidate identities, %d inserted, %d already present",
		r.Candidates, r.Inserted, r.Skipped)
}

// backfillCandidate is one most-recently-fetched articles row per
// (source_type, source_id, external-id-or-canonical-url) group — see the
// design doc §10 for why this grouping, and why last-fetched wins.
type backfillCandidate struct {
	articleID, sourceType, sourceID, externalID, canonicalURL, title, summary, status string
	fetchedAt                                                                         time.Time
}

// Run backfills seen_items rows for an installation's
// pre-existing articles history, reusing the real
// ingestidentity.Resolve/ContentHash and SeenItemsRepository.Claim so the
// backfilled keys are byte-identical to what the running application would
// compute — see the design doc §10 for why this is a Go command rather
// than a raw-SQL migration (SQLite cannot replicate SHA-256). Idempotent:
// re-running skips any identity that already has a row.
func Run(ctx context.Context, db *sql.DB, now time.Time) (Result, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, source_type, source_id, external_id, canonical_url, title, summary, status, fetched_at
		FROM (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY source_type, source_id, COALESCE(NULLIF(external_id, ''), canonical_url)
				ORDER BY fetched_at DESC, id DESC
			) AS rn
			FROM articles
		)
		WHERE rn = 1
	`)
	if err != nil {
		return Result{}, fmt.Errorf("seenitemsbackfill: query candidates: %w", err)
	}
	defer rows.Close()

	var candidates []backfillCandidate
	for rows.Next() {
		var c backfillCandidate
		if err := rows.Scan(&c.articleID, &c.sourceType, &c.sourceID, &c.externalID, &c.canonicalURL, &c.title, &c.summary, &c.status, &c.fetchedAt); err != nil {
			return Result{}, fmt.Errorf("seenitemsbackfill: scan candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("seenitemsbackfill: iterate candidates: %w", err)
	}

	seenItems := sqliteadapter.NewSeenItemsRepository(db)
	result := Result{Candidates: len(candidates)}
	for _, c := range candidates {
		identity := ingestidentity.Resolve(c.sourceType, c.sourceID, c.externalID, c.canonicalURL)
		if identity.Key == "" {
			continue // no usable identity (degenerate row with neither an external id nor a canonical url) — nothing safe to backfill
		}
		contentHash := ingestidentity.ContentHash(c.canonicalURL, c.title, c.summary)
		claim, err := seenItems.Claim(ctx, identity, c.articleID, contentHash, c.canonicalURL, c.fetchedAt, 0)
		if err != nil {
			return result, fmt.Errorf("seenitemsbackfill: claim %s/%s: %w", c.sourceType, c.sourceID, err)
		}
		if claim.Status != appout.SeenClaimNew {
			result.Skipped++
			continue
		}
		if err := seenItems.Finalize(ctx, identity, c.status, contentHash, c.canonicalURL, c.fetchedAt); err != nil {
			return result, fmt.Errorf("seenitemsbackfill: finalize %s/%s: %w", c.sourceType, c.sourceID, err)
		}
		result.Inserted++
	}
	return result, nil
}
