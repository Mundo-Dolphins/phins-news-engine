package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SeenItemsRepository implements ports/out.SeenItemsRepository against
// SQLite.
type SeenItemsRepository struct {
	db *sql.DB
}

// NewSeenItemsRepository builds a SeenItemsRepository over an
// already-open, already-migrated database.
func NewSeenItemsRepository(db *sql.DB) *SeenItemsRepository {
	return &SeenItemsRepository{db: db}
}

var _ appout.SeenItemsRepository = (*SeenItemsRepository)(nil)

// Claim implements ports/out.SeenItemsRepository. The INSERT's
// RowsAffected (not a follow-up read) is the single source of truth for
// whether this call's own insert won the identity — a value-equality
// check against the freshly-inserted row would be fragile under timestamp
// round-tripping through the driver.
func (r *SeenItemsRepository) Claim(ctx context.Context, identity ingestidentity.Identity, articleID, contentHash, canonicalURL string, now time.Time, reclaimAfter time.Duration) (appout.SeenClaim, error) {
	ex := executor(ctx, r.db)

	res, err := ex.ExecContext(ctx, `
		INSERT INTO seen_items (
			id, platform, source_id, identity_basis, identity_key, identity_algorithm,
			article_id, canonical_url, first_seen_at, last_seen_at, seen_count,
			terminal_outcome, content_hash, revision
		) VALUES (?,?,?,?,?,?, ?,?,?,?,1, '',?,1)
		ON CONFLICT(platform, source_id, identity_key) DO NOTHING
	`,
		uuid.NewString(), identity.Platform, identity.SourceID, string(identity.Basis), identity.Key, identity.Algorithm,
		articleID, canonicalURL, sqliteTime(now), sqliteTime(now), contentHash,
	)
	if err != nil {
		return appout.SeenClaim{}, fmt.Errorf("sqlite: claim seen_item %s/%s/%s: %w", identity.Platform, identity.SourceID, identity.Key, err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return appout.SeenClaim{}, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if inserted == 1 {
		return appout.SeenClaim{Status: appout.SeenClaimNew, ArticleID: articleID, SeenCount: 1, Revision: 1, FirstSeenAt: now}, nil
	}

	var (
		existingArticleID, terminalOutcome, existingHash, existingCanonicalURL string
		seenCount, revision                                                    int
		firstSeenAt, lastSeenAt                                                sqliteNullTime
	)
	row := ex.QueryRowContext(ctx, `
		SELECT article_id, terminal_outcome, content_hash, canonical_url, seen_count, revision, first_seen_at, last_seen_at
		FROM seen_items WHERE platform = ? AND source_id = ? AND identity_key = ?
	`, identity.Platform, identity.SourceID, identity.Key)
	if err := row.Scan(&existingArticleID, &terminalOutcome, &existingHash, &existingCanonicalURL, &seenCount, &revision, &firstSeenAt, &lastSeenAt); err != nil {
		return appout.SeenClaim{}, fmt.Errorf("sqlite: read claimed seen_item %s/%s/%s: %w", identity.Platform, identity.SourceID, identity.Key, err)
	}

	touch := func() error {
		_, err := ex.ExecContext(ctx, `
			UPDATE seen_items SET last_seen_at = ?, seen_count = seen_count + 1
			WHERE platform = ? AND source_id = ? AND identity_key = ?
			`, sqliteTime(now), identity.Platform, identity.SourceID, identity.Key)
		return err
	}

	switch {
	case terminalOutcome == "" && now.Sub(lastSeenAt.Time) < reclaimAfter:
		return appout.SeenClaim{
			Status: appout.SeenClaimInFlight, ArticleID: existingArticleID,
			SeenCount: seenCount, Revision: revision, FirstSeenAt: firstSeenAt.Time,
		}, nil
	case terminalOutcome == "": // stale in-flight claim: reclaim it
		if err := touch(); err != nil {
			return appout.SeenClaim{}, fmt.Errorf("sqlite: reclaim stale seen_item %s/%s/%s: %w", identity.Platform, identity.SourceID, identity.Key, err)
		}
		return appout.SeenClaim{
			Status: appout.SeenClaimNew, ArticleID: existingArticleID,
			SeenCount: seenCount + 1, Revision: revision, FirstSeenAt: firstSeenAt.Time,
		}, nil
	case existingHash == contentHash:
		if err := touch(); err != nil {
			return appout.SeenClaim{}, fmt.Errorf("sqlite: touch seen_item %s/%s/%s: %w", identity.Platform, identity.SourceID, identity.Key, err)
		}
		return appout.SeenClaim{
			Status: appout.SeenClaimSeen, ArticleID: existingArticleID, TerminalOutcome: terminalOutcome,
			PreviousContentHash: existingHash, PreviousCanonicalURL: existingCanonicalURL,
			SeenCount: seenCount + 1, Revision: revision, FirstSeenAt: firstSeenAt.Time,
		}, nil
	default:
		return appout.SeenClaim{
			Status: appout.SeenClaimRevised, ArticleID: existingArticleID, TerminalOutcome: terminalOutcome,
			PreviousContentHash: existingHash, PreviousCanonicalURL: existingCanonicalURL,
			SeenCount: seenCount, Revision: revision, FirstSeenAt: firstSeenAt.Time,
		}, nil
	}
}

// Finalize implements ports/out.SeenItemsRepository. revision increments
// only when contentHash differs from the row's pre-update content_hash —
// SQL's UPDATE evaluates every SET expression against the row's old
// values, so the CASE WHEN below is always comparing against the value
// content_hash had before this statement, never the new one.
func (r *SeenItemsRepository) Finalize(ctx context.Context, identity ingestidentity.Identity, outcome, contentHash, canonicalURL string, now time.Time) error {
	res, err := executor(ctx, r.db).ExecContext(ctx, `
		UPDATE seen_items SET
			terminal_outcome = ?,
			canonical_url = ?,
			last_seen_at = ?,
			updated_at = CURRENT_TIMESTAMP,
			revision = CASE WHEN content_hash = ? THEN revision ELSE revision + 1 END,
			content_hash = ?
		WHERE platform = ? AND source_id = ? AND identity_key = ?
		`, outcome, canonicalURL, sqliteTime(now), contentHash, contentHash, identity.Platform, identity.SourceID, identity.Key)
	if err != nil {
		return fmt.Errorf("sqlite: finalize seen_item %s/%s/%s: %w", identity.Platform, identity.SourceID, identity.Key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: finalize seen_item %s/%s/%s: no claimed row found", identity.Platform, identity.SourceID, identity.Key)
	}
	return nil
}
