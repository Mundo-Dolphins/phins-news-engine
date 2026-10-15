package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// seedArticleAndOutboxItem seeds a minimal valid article and a matching
// claimable publication_outbox row (status='pending', available_at safely
// in the past) for articleID, mirroring the mustSaveArticle +
// OutboxRepository.Enqueue pattern every other test in this file already
// uses.
func seedArticleAndOutboxItem(t *testing.T, db *sql.DB, articleID string) {
	t.Helper()
	articles := sqliteadapter.NewArticleRepository(db)
	mustSaveArticle(t, articles, articleID)
	outbox := sqliteadapter.NewOutboxRepository(db)
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := outbox.Enqueue(context.Background(), articleID, "{}", past); err != nil {
		t.Fatalf("Enqueue(%s) error = %v", articleID, err)
	}
}

// mustOutboxID looks up the real autoincremented publication_outbox.id for
// articleID — RecordAttempt's outbox_id column has a foreign key onto
// publication_outbox(id) (enforced: this project opens SQLite with
// _pragma=foreign_keys(ON)), so tests that seed attempts directly must use
// the row's real id rather than a guessed sequence number.
func mustOutboxID(t *testing.T, db *sql.DB, articleID string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM publication_outbox WHERE article_id = ?`, articleID).Scan(&id); err != nil {
		t.Fatalf("look up outbox id for %s: %v", articleID, err)
	}
	return id
}

func TestOutboxRepository_Enqueue_IsIdempotent(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Now()
	mustSaveArticle(t, articles, "a1")

	if err := repo.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	if err := repo.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("second Enqueue() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox WHERE article_id = 'a1'`).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (Enqueue must be idempotent)", count)
	}
}

func TestOutboxRepository_LanePersistsAndClaimIsFair(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	repo.SetLiveTTL(30 * time.Minute)
	now := time.Now().UTC()
	for _, id := range []string{"normal-1", "live-1", "live-2"} {
		mustSaveArticle(t, articles, id)
	}
	if err := repo.Enqueue(context.Background(), "normal-1", "{}", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnqueueLane(context.Background(), "live-1", "{}", publishing.LaneLive, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnqueueLane(context.Background(), "live-2", "{}", publishing.LaneLive, now); err != nil {
		t.Fatal(err)
	}
	var lane string
	if err := db.QueryRow(`SELECT lane FROM publication_outbox WHERE article_id='normal-1'`).Scan(&lane); err != nil {
		t.Fatal(err)
	}
	if lane != "normal" {
		t.Fatalf("legacy enqueue lane = %q", lane)
	}
	items, err := repo.ClaimBatch(context.Background(), "fair-worker", 2, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("claimed %d items, want 2", len(items))
	}
	seenLive := false
	for _, item := range items {
		if item.Lane == publishing.LaneLive {
			seenLive = true
		}
	}
	if !seenLive {
		t.Fatalf("fair batch omitted live lane: %+v", items)
	}
}

func TestOutboxRepository_EnqueuePersistsImmutableExpiryAndDeferPreservesIt(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	publishedAt := now.Add(-time.Hour)
	if err := articles.Save(context.Background(), article.Article{
		ID: "fresh", OriginalURL: "https://example.com/fresh", CanonicalURL: "https://example.com/fresh",
		Source:    source.Source{ID: mustID(t, source.TypeRSS, "expiry-test"), Type: source.TypeRSS},
		FetchedAt: publishedAt, PublishedAt: publishedAt, FreshnessMaxAge: 2 * time.Hour, Status: article.StatusFetched,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Enqueue(context.Background(), "fresh", "{}", now); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := db.QueryRow(`SELECT expires_at FROM publication_outbox WHERE article_id = 'fresh'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	want := publishedAt.Add(2 * time.Hour)
	if !before.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", before, want)
	}
	if _, err := db.Exec(`UPDATE publication_outbox SET status = 'leased'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Defer(context.Background(), 1, now.Add(24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := db.QueryRow(`SELECT expires_at FROM publication_outbox WHERE article_id = 'fresh'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Errorf("defer changed expires_at from %v to %v", before, after)
	}
}

func TestOutboxRepository_ClaimBatchExcludesExpiredRows(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		id        string
		published time.Time
	}{
		{"expired", now.Add(-3 * time.Hour)},
		{"live", now.Add(-time.Hour)},
	} {
		if err := articles.Save(context.Background(), article.Article{
			ID: tc.id, OriginalURL: "https://example.com/" + tc.id, CanonicalURL: "https://example.com/" + tc.id,
			Source:    source.Source{ID: mustID(t, source.TypeRSS, "claim-"+tc.id), Type: source.TypeRSS},
			FetchedAt: tc.published, PublishedAt: tc.published, FreshnessMaxAge: 2 * time.Hour, Status: article.StatusFetched,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repo.Enqueue(context.Background(), tc.id, "{}", now); err != nil {
			t.Fatal(err)
		}
	}
	items, err := repo.ClaimBatch(context.Background(), "worker", 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ArticleID != "live" {
		t.Fatalf("claimed = %+v, want only live", items)
	}
}

func TestOutboxRepository_ClaimExpiredLeasesOnlyKnownExpiredClaimableRows(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		id        string
		published time.Time
		maxAge    time.Duration
	}{
		{"expired-deferred", now.Add(-3 * time.Hour), 2 * time.Hour},
		{"expired-retryable", now.Add(-3 * time.Hour), 2 * time.Hour},
		{"expired-delivered", now.Add(-3 * time.Hour), 2 * time.Hour},
		{"fresh", now.Add(-time.Hour), 2 * time.Hour},
		{"unknown-expiry", now.Add(-30 * 24 * time.Hour), 0},
	} {
		if err := articles.Save(context.Background(), article.Article{
			ID: tc.id, OriginalURL: "https://example.com/" + tc.id, CanonicalURL: "https://example.com/" + tc.id,
			Source:    source.Source{ID: mustID(t, source.TypeRSS, "sweep-"+tc.id), Type: source.TypeRSS},
			FetchedAt: tc.published, PublishedAt: tc.published, FreshnessMaxAge: tc.maxAge, Status: article.StatusFetched,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repo.Enqueue(context.Background(), tc.id, "{}", now); err != nil {
			t.Fatal(err)
		}
	}
	// A policy defer pushes available_at past the deadline: the sweep must
	// still pick the row up, since ClaimBatch never will.
	if _, err := db.Exec(`UPDATE publication_outbox SET available_at = ? WHERE article_id = 'expired-deferred'`, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE publication_outbox SET status = 'retryable' WHERE article_id = 'expired-retryable'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE publication_outbox SET status = 'delivered' WHERE article_id = 'expired-delivered'`); err != nil {
		t.Fatal(err)
	}

	items, err := repo.ClaimExpired(context.Background(), "worker", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.ArticleID] = true
		if it.Status != publishing.OutboxLeased || it.ClaimedBy != "worker" {
			t.Errorf("item %s = status %q claimed_by %q, want leased by worker", it.ArticleID, it.Status, it.ClaimedBy)
		}
	}
	if len(items) != 2 || !got["expired-deferred"] || !got["expired-retryable"] {
		t.Fatalf("claimed = %+v, want expired-deferred and expired-retryable only", items)
	}

	limited, err := repo.ClaimExpired(context.Background(), "worker-2", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 0 {
		t.Fatalf("second sweep claimed %+v, want none: rows are already leased", limited)
	}
}

func TestOutboxRepository_ClaimPlanUsesStatusAvailabilityExpiryIndex(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN
		SELECT po.id FROM publication_outbox po JOIN articles a ON a.id = po.article_id
		WHERE po.status IN ('pending', 'retryable') AND po.available_at <= ?
		  AND (po.expires_at IS NULL OR po.expires_at > ?)
		ORDER BY CASE WHEN a.published_at IS NULL THEN 1 ELSE 0 END ASC,
		         a.published_at ASC,
		         CASE WHEN a.published_at IS NULL THEN a.created_at END ASC,
		         po.id ASC LIMIT ?`, time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var used bool
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "idx_outbox_status_available_expires") {
			used = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("claim query plan does not use idx_outbox_status_available_expires")
	}
}

func TestOutboxRepository_ClaimBatch_NoDoubleClaimUnderConcurrency(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Now()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("a%d", i)
		mustSaveArticle(t, articles, id)
		if err := repo.Enqueue(ctx, id, "{}", now); err != nil {
			t.Fatalf("Enqueue(%s) error = %v", id, err)
		}
	}

	var (
		mu      sync.Mutex
		claimed = map[string]bool{}
		wg      sync.WaitGroup
	)
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			items, err := repo.ClaimBatch(ctx, workerID, 3, time.Minute, now)
			if err != nil {
				t.Errorf("ClaimBatch(%s) error = %v", workerID, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, it := range items {
				if claimed[it.ArticleID] {
					t.Errorf("article %s claimed by more than one worker", it.ArticleID)
				}
				claimed[it.ArticleID] = true
			}
		}(fmt.Sprintf("worker-%d", w))
	}
	wg.Wait()

	if len(claimed) != total {
		t.Errorf("claimed %d distinct articles across all workers, want %d", len(claimed), total)
	}
}

func TestOutboxRepository_ClaimBatch_ReclaimsStaleClaims(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Now()
	mustSaveArticle(t, articles, "a1")
	if err := repo.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	first, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(first) != 1 {
		t.Fatalf("first ClaimBatch() = %v, %v, want 1 item", first, err)
	}

	// Simulate worker-1 crashing: worker-2 should see nothing yet, since
	// staleAfter (1 minute) hasn't elapsed.
	second, err := repo.ClaimBatch(ctx, "worker-2", 10, time.Minute, now)
	if err != nil {
		t.Fatalf("second ClaimBatch() error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second ClaimBatch() len = %d, want 0 (claim still fresh)", len(second))
	}

	later := now.Add(2 * time.Minute)
	third, err := repo.ClaimBatch(ctx, "worker-2", 10, time.Minute, later)
	if err != nil {
		t.Fatalf("third ClaimBatch() error = %v", err)
	}
	if len(third) != 1 || third[0].ArticleID != "a1" {
		t.Fatalf("third ClaimBatch() = %+v, want a1 reclaimed by worker-2", third)
	}
}

func TestOutboxRepository_MarkDelivered(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Now()
	mustSaveArticle(t, articles, "a1")
	if err := repo.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	items, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v", items, err)
	}

	if err := repo.MarkDelivered(ctx, items[0].ID, now); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}

	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM publication_outbox WHERE id = ?`, items[0].ID).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != "delivered" {
		t.Errorf("status = %q, want %q", status, "delivered")
	}
}

func TestOutboxRepository_MarkRetryable_RetryThenDead(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Now()
	mustSaveArticle(t, articles, "a1")
	if err := repo.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	items, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v", items, err)
	}

	if err := repo.MarkRetryable(ctx, items[0].ID, "temporary network error", now.Add(30*time.Second), false, now); err != nil {
		t.Fatalf("MarkRetryable(retry) error = %v", err)
	}

	var status string
	var attemptCount int
	if err := db.QueryRowContext(ctx, `SELECT status, attempt_count FROM publication_outbox WHERE id = ?`, items[0].ID).Scan(&status, &attemptCount); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "retryable" || attemptCount != 1 {
		t.Errorf("status/attempt_count = %q/%d, want retryable/1", status, attemptCount)
	}

	// A retryable item's next ClaimBatch pass (once due) must pick it back
	// up, same as a never-attempted pending row.
	claimed, err := repo.ClaimBatch(ctx, "worker-2", 10, time.Minute, now.Add(time.Minute))
	if err != nil || len(claimed) != 1 || claimed[0].ID != items[0].ID {
		t.Fatalf("ClaimBatch() after MarkRetryable = %+v, %v, want the retryable item reclaimed", claimed, err)
	}

	if err := repo.MarkRetryable(ctx, items[0].ID, "permanent failure", time.Time{}, true, now); err != nil {
		t.Fatalf("MarkRetryable(dead) error = %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, attempt_count FROM publication_outbox WHERE id = ?`, items[0].ID).Scan(&status, &attemptCount); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "dead" || attemptCount != 2 {
		t.Errorf("status/attempt_count = %q/%d, want dead/2", status, attemptCount)
	}
}

func TestOutboxRepository_RecordAttemptAndDeleteOldAttempts(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Now()
	mustSaveArticle(t, articles, "a1")
	if err := repo.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	items, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v", items, err)
	}

	old := appout.Attempt{OutboxID: items[0].ID, ArticleID: "a1", AttemptedAt: now.Add(-48 * time.Hour), Outcome: publishing.AttemptTemporaryFailure, ErrorMessage: "timeout"}
	recent := appout.Attempt{OutboxID: items[0].ID, ArticleID: "a1", AttemptedAt: now, Outcome: publishing.AttemptSucceeded}
	if err := repo.RecordAttempt(ctx, old); err != nil {
		t.Fatalf("RecordAttempt(old) error = %v", err)
	}
	if err := repo.RecordAttempt(ctx, recent); err != nil {
		t.Fatalf("RecordAttempt(recent) error = %v", err)
	}

	deleted, err := repo.DeleteOldAttempts(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteOldAttempts() error = %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteOldAttempts() = %d, want 1", deleted)
	}

	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_attempts WHERE outbox_id = ?`, items[0].ID).Scan(&remaining); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining attempts = %d, want 1", remaining)
	}
}

func TestOutboxRepository_DeferReturnsItemToPendingWithoutIncrementingAttempts(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	if err := articles.Save(ctx, article.Article{ID: "a1", CanonicalURL: "https://example.com/a1", PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := outbox.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := outbox.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v, want 1 claimed item", claimed, err)
	}

	retryAt := now.Add(time.Hour)
	if err := outbox.Defer(ctx, claimed[0].ID, retryAt, now); err != nil {
		t.Fatalf("Defer() error = %v", err)
	}

	// A second ClaimBatch before retryAt must not reclaim it.
	tooSoon, err := outbox.ClaimBatch(ctx, "worker-1", 10, time.Minute, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	if len(tooSoon) != 0 {
		t.Errorf("ClaimBatch() before retryAt = %v, want none claimed", tooSoon)
	}

	// After retryAt, it's claimable again, and attempt_count never moved.
	afterRetry, err := outbox.ClaimBatch(ctx, "worker-1", 10, time.Minute, retryAt)
	if err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	if len(afterRetry) != 1 || afterRetry[0].AttemptCount != 0 {
		t.Errorf("ClaimBatch() after retryAt = %+v, want 1 item with AttemptCount=0", afterRetry)
	}
}

func TestOutboxRepository_CountSucceededSince(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seed := []struct {
		id      string
		attempt time.Time
		outcome publishing.AttemptOutcome
	}{
		{"old", now.Add(-2 * time.Hour), publishing.AttemptSucceeded},
		{"recent1", now.Add(-10 * time.Minute), publishing.AttemptSucceeded},
		{"recent2", now.Add(-5 * time.Minute), publishing.AttemptSucceeded},
		{"failed", now.Add(-1 * time.Minute), publishing.AttemptTemporaryFailure},
	}
	for _, s := range seed {
		if err := articles.Save(ctx, article.Article{ID: s.id, CanonicalURL: "https://example.com/" + s.id, PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication}); err != nil {
			t.Fatalf("seed article %s: %v", s.id, err)
		}
		if err := outbox.Enqueue(ctx, s.id, "{}", now); err != nil {
			t.Fatalf("Enqueue(%s): %v", s.id, err)
		}
		outboxID := mustOutboxID(t, db, s.id)
		if err := outbox.RecordAttempt(ctx, appout.Attempt{
			OutboxID: outboxID, ArticleID: s.id, AttemptedAt: s.attempt,
			Outcome: s.outcome,
		}); err != nil {
			t.Fatalf("RecordAttempt(%s): %v", s.id, err)
		}
	}

	count, err := outbox.CountSucceededSince(ctx, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("CountSucceededSince() error = %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (only recent1/recent2: succeeded, within the last hour)", count)
	}
}

func TestOutboxRepository_CountSucceededByPrimaryEntitySince(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seed := []struct {
		id     string
		entity string
	}{
		{"jane1", "Jane Doe"},
		{"jane2", "Jane Doe"},
		{"john1", "John Smith"},
	}
	for _, s := range seed {
		if err := articles.Save(ctx, article.Article{ID: s.id, CanonicalURL: "https://example.com/" + s.id, PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication, PrimaryEntity: s.entity}); err != nil {
			t.Fatalf("seed article %s: %v", s.id, err)
		}
		if err := outbox.Enqueue(ctx, s.id, "{}", now); err != nil {
			t.Fatalf("Enqueue(%s): %v", s.id, err)
		}
		outboxID := mustOutboxID(t, db, s.id)
		if err := outbox.RecordAttempt(ctx, appout.Attempt{
			OutboxID: outboxID, ArticleID: s.id, AttemptedAt: now.Add(-5 * time.Minute),
			Outcome: publishing.AttemptSucceeded,
		}); err != nil {
			t.Fatalf("RecordAttempt(%s): %v", s.id, err)
		}
	}

	count, err := outbox.CountSucceededByPrimaryEntitySince(ctx, "Jane Doe", now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("CountSucceededByPrimaryEntitySince() error = %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (only Jane Doe rows)", count)
	}
}

func TestOutboxRepository_ClaimBatchOrdersChronologicallyNotByEditorialPriority(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	seed := []article.Article{
		{ID: "old", CanonicalURL: "https://example.com/old", PublishedAt: now.Add(-2 * time.Hour), FetchedAt: now, Status: article.StatusPendingPublication, EditorialScore: 1},
		{ID: "trusted-new", CanonicalURL: "https://example.com/trusted-new", PublishedAt: now.Add(-time.Hour), FetchedAt: now, Status: article.StatusPendingPublication, EditorialScore: 100, IsTrustedSource: true},
		{ID: "new", CanonicalURL: "https://example.com/new", PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication, EditorialScore: 90},
	}
	for _, a := range seed {
		if err := articles.Save(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", a.ID, err)
		}
		// Enqueue in reverse-priority order so ordering cannot be an accident of insertion order.
		if err := outbox.Enqueue(ctx, a.ID, "{}", now); err != nil {
			t.Fatalf("Enqueue(%s): %v", a.ID, err)
		}
	}

	claimed, err := outbox.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed = %v, want 3", claimed)
	}
	want := []string{"old", "trusted-new", "new"}
	for i, id := range want {
		if claimed[i].ArticleID != id {
			t.Errorf("claimed[%d].ArticleID = %q, want %q (order: %v)", i, claimed[i].ArticleID, id, claimed)
		}
	}
}

func TestOutboxRepository_ClaimBatchDeferralDoesNotChangeChronologicalPosition(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	for _, a := range []article.Article{
		{ID: "old", CanonicalURL: "https://example.com/old", PublishedAt: now.Add(-2 * time.Hour), FetchedAt: now, Status: article.StatusPendingPublication},
		{ID: "new", CanonicalURL: "https://example.com/new", PublishedAt: now.Add(-time.Hour), FetchedAt: now, Status: article.StatusPendingPublication, IsTrustedSource: true},
	} {
		if err := articles.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := outbox.Enqueue(ctx, a.ID, "{}", now); err != nil {
			t.Fatal(err)
		}
	}

	first, err := outbox.ClaimBatch(ctx, "worker-1", 1, time.Minute, now)
	if err != nil || len(first) != 1 || first[0].ArticleID != "old" {
		t.Fatalf("first ClaimBatch() = %+v, %v, want old", first, err)
	}
	deferredUntil := now.Add(time.Hour)
	if err := outbox.Defer(ctx, first[0].ID, deferredUntil, now); err != nil {
		t.Fatal(err)
	}

	second, err := outbox.ClaimBatch(ctx, "worker-1", 1, time.Minute, now)
	if err != nil || len(second) != 1 || second[0].ArticleID != "new" {
		t.Fatalf("second ClaimBatch() = %+v, %v, want new while old is deferred", second, err)
	}
	if err := outbox.Defer(ctx, second[0].ID, deferredUntil, now); err != nil {
		t.Fatal(err)
	}

	final, err := outbox.ClaimBatch(ctx, "worker-2", 2, time.Minute, deferredUntil)
	if err != nil || len(final) != 2 {
		t.Fatalf("final ClaimBatch() = %+v, %v, want both deferred items", final, err)
	}
	if final[0].ArticleID != "old" || final[1].ArticleID != "new" {
		t.Fatalf("final order = [%s, %s], want [old, new]", final[0].ArticleID, final[1].ArticleID)
	}
}

func TestOutboxRepository_ClaimBatchChronologicalTieBreakers(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	seed := []struct {
		id        string
		published *time.Time
		created   time.Time
	}{
		{id: "known", published: timePtr(now), created: now},
		{id: "null-old", published: nil, created: now.Add(-2 * time.Hour)},
		{id: "null-new", published: nil, created: now.Add(-time.Hour)},
		{id: "same-b", published: timePtr(now), created: now},
		{id: "same-a", published: timePtr(now), created: now},
	}
	for _, s := range seed {
		var published time.Time
		if s.published == nil {
			published = time.Time{}
		} else {
			published = *s.published
		}
		if err := articles.Save(ctx, article.Article{ID: s.id, CanonicalURL: "https://example.com/" + s.id, PublishedAt: published, FetchedAt: now, Status: article.StatusPendingPublication}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE articles SET created_at = ? WHERE id = ?`, s.created, s.id); err != nil {
			t.Fatal(err)
		}
		if err := outbox.Enqueue(ctx, s.id, "{}", now); err != nil {
			t.Fatal(err)
		}
	}

	claimed, err := outbox.ClaimBatch(ctx, "worker-1", 5, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(claimed), 5; got != want {
		t.Fatalf("claimed %d items, want %d", got, want)
	}
	// published_at wins; NULL published_at falls back to articles.created_at;
	// the final outbox id tie-breaker is exercised by the equal-time rows.
	want := []string{"known", "same-b", "same-a", "null-old", "null-new"}
	for i, id := range want {
		if claimed[i].ArticleID != id {
			t.Errorf("claimed[%d].ArticleID = %q, want %q (order: %v)", i, claimed[i].ArticleID, id, claimed)
		}
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func TestOutboxRepository_RecordSentUnconfirmedThenMarkDelivered(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedArticleAndOutboxItem(t, db, "a1")

	items, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v", items, err)
	}
	id := items[0].ID

	if err := repo.RecordSentUnconfirmed(ctx, id, "msg-123", "", now); err != nil {
		t.Fatalf("RecordSentUnconfirmed() error = %v", err)
	}

	// A stale reclaim must NOT touch this row: it is no longer 'leased'.
	reclaimed, err := repo.ClaimBatch(ctx, "worker-2", 10, 0, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ClaimBatch() (reclaim pass) error = %v", err)
	}
	for _, it := range reclaimed {
		if it.ID == id {
			t.Fatal("sent_unconfirmed row was reclaimed and re-offered for publish — blind resend risk")
		}
	}

	pending, err := repo.ListSentUnconfirmed(ctx, 10, now.Add(time.Hour))
	if err != nil || len(pending) != 1 || pending[0].SentMessageID != "msg-123" {
		t.Fatalf("ListSentUnconfirmed() = %+v, %v, want 1 item with SentMessageID=msg-123", pending, err)
	}

	if err := repo.MarkDelivered(ctx, id, now.Add(time.Hour)); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}
	after, err := repo.ListSentUnconfirmed(ctx, 10, now.Add(2*time.Hour))
	if err != nil || len(after) != 0 {
		t.Fatalf("ListSentUnconfirmed() after MarkDelivered = %+v, want empty", after)
	}
}

// TestOutboxRepository_LostLeaseIsALoudError covers the failure mode both
// of these writes used to swallow: their UPDATEs are guarded on
// status IN ('leased','sent_unconfirmed'), and neither checked
// RowsAffected, so an item moved out from under an in-flight send made
// them silently no-op while the caller carried on as if the write had
// landed. RecordSentUnconfirmed is ADR 0030's crash-safety anchor — a
// silent no-op there leaves no evidence Telegram may already have the
// message, which is precisely the blind-resend window this feature closes.
func TestOutboxRepository_LostLeaseIsALoudError(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(repo *sqliteadapter.OutboxRepository, ctx context.Context, id int64, now time.Time) error
	}{
		{"RecordSentUnconfirmed", func(repo *sqliteadapter.OutboxRepository, ctx context.Context, id int64, now time.Time) error {
			return repo.RecordSentUnconfirmed(ctx, id, "msg-123", "", now)
		}},
		{"MarkDelivered", func(repo *sqliteadapter.OutboxRepository, ctx context.Context, id int64, now time.Time) error {
			return repo.MarkDelivered(ctx, id, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			repo := sqliteadapter.NewOutboxRepository(db)
			ctx := context.Background()
			now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
			seedArticleAndOutboxItem(t, db, "a1")

			items, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
			if err != nil || len(items) != 1 {
				t.Fatalf("ClaimBatch() = %v, %v", items, err)
			}
			id := items[0].ID

			// The lease effectively vanishes: something else moved the row
			// to 'retryable', which neither guard accepts.
			if err := repo.MarkRetryable(ctx, id, "reclaimed elsewhere", now.Add(time.Minute), false, now); err != nil {
				t.Fatalf("MarkRetryable() error = %v", err)
			}

			err = tc.call(repo, ctx, id, now)
			if !errors.Is(err, appout.ErrLeaseLost) {
				t.Fatalf("%s() after the lease was lost = %v, want appout.ErrLeaseLost", tc.name, err)
			}
		})
	}
}

func TestOutboxRepository_ClaimBatchIncludesRetryable(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedArticleAndOutboxItem(t, db, "a1")

	items, _ := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	id := items[0].ID
	if err := repo.MarkRetryable(ctx, id, "temporary", now.Add(time.Minute), false, now); err != nil {
		t.Fatalf("MarkRetryable() error = %v", err)
	}

	claimed, err := repo.ClaimBatch(ctx, "worker-2", 10, time.Minute, now.Add(2*time.Minute))
	if err != nil || len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("ClaimBatch() after MarkRetryable = %+v, %v, want the retryable item reclaimable once due", claimed, err)
	}
}

func TestOutboxRepository_QuarantineAndListQuarantined(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedArticleAndOutboxItem(t, db, "a1")

	items, _ := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	id := items[0].ID
	if err := repo.RecordSentUnconfirmed(ctx, id, "", "timeout", now); err != nil {
		t.Fatalf("RecordSentUnconfirmed() error = %v", err)
	}
	if err := repo.Quarantine(ctx, id, "channel_history_inconclusive", now.Add(time.Minute)); err != nil {
		t.Fatalf("Quarantine() error = %v", err)
	}

	list, err := repo.ListQuarantined(ctx, 10)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListQuarantined() = %+v, %v, want [id=%d]", list, err, id)
	}

	// A leased/pending item can never be quarantined directly (only
	// sent_unconfirmed can) — this guards against a future caller
	// accidentally quarantining an item that was never actually sent.
	seedArticleAndOutboxItem(t, db, "a2")
	items2, _ := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	var leasedID int64
	for _, it := range items2 {
		if it.ArticleID == "a2" {
			leasedID = it.ID
		}
	}
	if err := repo.Quarantine(ctx, leasedID, "should not apply", now); err != nil {
		t.Fatalf("Quarantine() on a leased item error = %v", err)
	}
	list2, _ := repo.ListQuarantined(ctx, 10)
	if len(list2) != 1 {
		t.Fatalf("ListQuarantined() = %+v, want still just the original sent_unconfirmed item (quarantine on a leased row must be a no-op)", list2)
	}
}

func TestOutboxRepository_RepairActions(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	seedArticleAndOutboxItem(t, db, "a1")
	items, _ := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	quarantinedID := items[0].ID
	_ = repo.RecordSentUnconfirmed(ctx, quarantinedID, "", "timeout", now)
	_ = repo.Quarantine(ctx, quarantinedID, "inconclusive", now)

	if err := repo.Repair(ctx, quarantinedID, "a1", publishing.RepairDeliver, "operator confirmed via channel", now.Add(time.Minute)); err != nil {
		t.Fatalf("Repair(deliver) error = %v", err)
	}
	counts, err := repo.StatusCounts(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("StatusCounts() error = %v", err)
	}
	if counts[publishing.OutboxDelivered].Count != 1 {
		t.Fatalf("StatusCounts() = %+v, want 1 delivered", counts)
	}

	if err := repo.Repair(ctx, quarantinedID, "a1", publishing.RepairRetry, "double repair", now); !errors.Is(err, appout.ErrNotRepairable) {
		t.Fatalf("Repair() on an already-delivered item error = %v, want ErrNotRepairable", err)
	}
}

// TestOutboxRepository_RepairRejectsMismatchedArticleID proves the guard
// that closes an operator paste-slip risk: repairing outbox item id with
// the wrong --article-id must fail loudly (ErrNotRepairable) rather than
// silently mutate a different article and its cluster-publication gate.
func TestOutboxRepository_RepairRejectsMismatchedArticleID(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	seedArticleAndOutboxItem(t, db, "a1")
	seedArticleAndOutboxItem(t, db, "a2")
	items, _ := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	var quarantinedID int64
	for _, it := range items {
		if it.ArticleID == "a1" {
			quarantinedID = it.ID
		}
	}
	_ = repo.RecordSentUnconfirmed(ctx, quarantinedID, "", "timeout", now)
	_ = repo.Quarantine(ctx, quarantinedID, "inconclusive", now)

	if err := repo.Repair(ctx, quarantinedID, "a2", publishing.RepairDeliver, "wrong article id", now); !errors.Is(err, appout.ErrNotRepairable) {
		t.Fatalf("Repair() with a mismatched article id error = %v, want ErrNotRepairable", err)
	}

	list, err := repo.ListQuarantined(ctx, 10)
	if err != nil || len(list) != 1 || list[0].ID != quarantinedID {
		t.Fatalf("ListQuarantined() = %+v, %v, want the item still quarantined and untouched", list, err)
	}
}

func TestOutboxRepository_StatusCounts_MultipleStatusesAndOldestAge(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	seedArticleAndOutboxItem(t, db, "a1")
	seedArticleAndOutboxItem(t, db, "a2")
	seedArticleAndOutboxItem(t, db, "a3")

	claimed, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("ClaimBatch() = %v, %v, want 3 claimed", claimed, err)
	}
	idByArticle := map[string]int64{}
	for _, it := range claimed {
		idByArticle[it.ArticleID] = it.ID
	}

	// a1 and a2 both land in 'sent_unconfirmed' (two rows in the same
	// status, so the per-status oldest-created_at phase has more than one
	// candidate row to pick from), a3 lands in 'delivered'.
	if err := repo.RecordSentUnconfirmed(ctx, idByArticle["a1"], "msg-1", "", now); err != nil {
		t.Fatalf("RecordSentUnconfirmed(a1) error = %v", err)
	}
	if err := repo.RecordSentUnconfirmed(ctx, idByArticle["a2"], "msg-2", "", now); err != nil {
		t.Fatalf("RecordSentUnconfirmed(a2) error = %v", err)
	}
	if err := repo.MarkDelivered(ctx, idByArticle["a3"], now); err != nil {
		t.Fatalf("MarkDelivered(a3) error = %v", err)
	}

	// Backdate a1's created_at directly so it is provably the older of the
	// two sent_unconfirmed rows, and pin the exact age StatusCounts must
	// report for that status.
	backdated := now.Add(-3 * time.Hour)
	if _, err := db.ExecContext(ctx, `UPDATE publication_outbox SET created_at = ? WHERE id = ?`, backdated, idByArticle["a1"]); err != nil {
		t.Fatalf("backdate a1 created_at: %v", err)
	}

	counts, err := repo.StatusCounts(ctx, now)
	if err != nil {
		t.Fatalf("StatusCounts() error = %v", err)
	}
	if got := counts[publishing.OutboxSentUnconfirmed].Count; got != 2 {
		t.Errorf("sent_unconfirmed count = %d, want 2", got)
	}
	if got, want := counts[publishing.OutboxSentUnconfirmed].OldestAge, 3*time.Hour; got != want {
		t.Errorf("sent_unconfirmed OldestAge = %v, want %v (the backdated a1 row)", got, want)
	}
	if got := counts[publishing.OutboxDelivered].Count; got != 1 {
		t.Errorf("delivered count = %d, want 1", got)
	}
}

// TestOutboxRepository_StatusCounts_ConcurrentWithStatusMutation is a
// regression guard for the race that motivated wrapping StatusCounts in a
// single withTx call: before that fix, StatusCounts ran its per-status
// COUNT query and its per-status oldest-created_at query as two separate,
// non-atomic statements. A concurrent status-mutating call
// (ClaimBatch/RecordSentUnconfirmed/MarkRetryable/...) could move every
// row of a status counted in phase one out of that status before phase
// two's ORDER BY/LIMIT 1 lookup for it ran, making that lookup observe
// zero rows and fail the whole StatusCounts call with sql.ErrNoRows — an
// operator-facing CLI command that would then intermittently error under
// ordinary concurrent worker load.
//
// This test drives a tight status-mutation cycle (sent_unconfirmed ->
// retryable -> leased -> sent_unconfirmed -> ...) on one row from a
// background goroutine while the main goroutine repeatedly calls
// StatusCounts, and asserts StatusCounts never errors. Under this
// project's SetMaxOpenConns(1) pool, an atomic (withTx) StatusCounts call
// holds the single connection for its entire duration, so the mutator can
// only ever run fully before or fully after any one StatusCounts call,
// never interleaved with it — which is exactly the property this test
// exercises. Without the withTx fix, this test is expected to eventually
// hit sql.ErrNoRows (confirmed manually against the pre-fix version).
func TestOutboxRepository_StatusCounts_ConcurrentWithStatusMutation(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	seedArticleAndOutboxItem(t, db, "a1")
	claimed, err := repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v, want 1 claimed", claimed, err)
	}
	id := claimed[0].ID

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Best-effort cycle: sent_unconfirmed -> retryable -> leased
			// -> sent_unconfirmed -> ... Errors are ignored here (e.g. a
			// step landing on a status where the next step is a no-op)
			// since only the concurrent contention matters, not making
			// every step succeed every time.
			_ = repo.RecordSentUnconfirmed(ctx, id, "msg", "", now)
			_ = repo.MarkRetryable(ctx, id, "temp", now, false, now)
			_, _ = repo.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
		}
	}()

	for i := 0; i < 300; i++ {
		if _, err := repo.StatusCounts(ctx, now); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("StatusCounts() under concurrent status mutation error = %v (iteration %d)", err, i)
		}
	}
	close(stop)
	wg.Wait()
}
