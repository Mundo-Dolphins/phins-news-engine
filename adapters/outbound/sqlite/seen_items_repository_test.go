package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func newSeenItemsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seen_items.db")
	copyMigratedTestDB(t, path)
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// insertArticleStub inserts the minimum articles row seen_items' foreign
// key requires, since SeenItemsRepository never itself writes to articles.
func insertArticleStub(t *testing.T, db *sql.DB, id string, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO articles (id, original_url, canonical_url, fetched_at, status)
		VALUES (?, 'https://example.com/x', 'https://example.com/x', ?, 'fetched')
	`, id, now)
	if err != nil {
		t.Fatalf("insert article stub: %v", err)
	}
}

func TestSeenItemsRepository_Claim_NewIdentity(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewSeenItemsRepository(db)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insertArticleStub(t, db, "article-1", now)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/x")

	claim, err := repo.Claim(context.Background(), identity, "article-1", "hash-1", "https://example.com/x", now, 10*time.Minute)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claim.Status != out.SeenClaimNew || claim.ArticleID != "article-1" {
		t.Fatalf("claim = %+v, want Status=new ArticleID=article-1", claim)
	}
}

func TestSeenItemsRepository_Claim_IdenticalRefetchIsSeen(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewSeenItemsRepository(db)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insertArticleStub(t, db, "article-1", now)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/x")

	if _, err := repo.Claim(context.Background(), identity, "article-1", "hash-1", "https://example.com/x", now, 10*time.Minute); err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}
	if err := repo.Finalize(context.Background(), identity, "rejected_not_related", "hash-1", "https://example.com/x", now); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	later := now.Add(time.Hour)
	claim, err := repo.Claim(context.Background(), identity, "should-be-ignored", "hash-1", "https://example.com/x", later, 10*time.Minute)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if claim.Status != out.SeenClaimSeen || claim.ArticleID != "article-1" || claim.TerminalOutcome != "rejected_not_related" {
		t.Fatalf("claim = %+v, want Status=seen ArticleID=article-1 TerminalOutcome=rejected_not_related", claim)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM seen_items`).Scan(&count); err != nil {
		t.Fatalf("count seen_items: %v", err)
	}
	if count != 1 {
		t.Fatalf("seen_items rows = %d, want 1 (no duplicate row from the second Claim)", count)
	}
}

func TestSeenItemsRepository_Claim_RevisedContentAfterTerminalOutcome(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewSeenItemsRepository(db)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insertArticleStub(t, db, "article-1", now)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/x")

	if _, err := repo.Claim(context.Background(), identity, "article-1", "hash-1", "https://example.com/x", now, 10*time.Minute); err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}
	if err := repo.Finalize(context.Background(), identity, "rejected_not_related", "hash-1", "https://example.com/x", now); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	later := now.Add(time.Hour)
	claim, err := repo.Claim(context.Background(), identity, "article-1", "hash-2", "https://example.com/x", later, 10*time.Minute)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if claim.Status != out.SeenClaimRevised || claim.PreviousContentHash != "hash-1" {
		t.Fatalf("claim = %+v, want Status=revised PreviousContentHash=hash-1", claim)
	}

	if err := repo.Finalize(context.Background(), identity, "pending_publication", "hash-2", "https://example.com/x", later); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	var revision int
	if err := db.QueryRow(`SELECT revision FROM seen_items WHERE identity_key = ?`, identity.Key).Scan(&revision); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if revision != 2 {
		t.Fatalf("revision = %d, want 2 after one content change", revision)
	}
}

func TestSeenItemsRepository_Claim_InFlightNotYetStale(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewSeenItemsRepository(db)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insertArticleStub(t, db, "article-1", now)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/x")

	// First Claim leaves terminal_outcome empty (never Finalized) —
	// simulating a worker that crashed mid-processing.
	if _, err := repo.Claim(context.Background(), identity, "article-1", "hash-1", "https://example.com/x", now, 10*time.Minute); err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}

	soon := now.Add(2 * time.Minute)
	claim, err := repo.Claim(context.Background(), identity, "article-2", "hash-1", "https://example.com/x", soon, 10*time.Minute)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if claim.Status != out.SeenClaimInFlight || claim.ArticleID != "article-1" {
		t.Fatalf("claim = %+v, want Status=in_flight ArticleID=article-1", claim)
	}
}

func TestSeenItemsRepository_Claim_StaleInFlightIsReclaimed(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewSeenItemsRepository(db)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insertArticleStub(t, db, "article-1", now)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/x")

	if _, err := repo.Claim(context.Background(), identity, "article-1", "hash-1", "https://example.com/x", now, 10*time.Minute); err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}

	muchLater := now.Add(time.Hour)
	claim, err := repo.Claim(context.Background(), identity, "article-2", "hash-1", "https://example.com/x", muchLater, 10*time.Minute)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if claim.Status != out.SeenClaimNew || claim.ArticleID != "article-1" {
		t.Fatalf("claim = %+v, want Status=new ArticleID=article-1 (reclaimed, original article_id preserved)", claim)
	}
}

func TestSeenItemsRepository_Claim_ConcurrentClaimsOnSameIdentityYieldExactlyOneNew(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewSeenItemsRepository(db)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insertArticleStub(t, db, "article-1", now)
	insertArticleStub(t, db, "article-2", now)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/x")

	const workers = 8
	statuses := make([]out.SeenClaimStatus, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			articleID := "article-1"
			if i%2 == 1 {
				articleID = "article-2"
			}
			claim, err := repo.Claim(context.Background(), identity, articleID, "hash-1", "https://example.com/x", now, 10*time.Minute)
			statuses[i], errs[i] = claim.Status, err
		}(i)
	}
	wg.Wait()

	newCount := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: Claim() error = %v", i, err)
		}
		if statuses[i] == out.SeenClaimNew {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("newCount = %d, want exactly 1 (identity claimed by exactly one worker)", newCount)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM seen_items`).Scan(&rows); err != nil {
		t.Fatalf("count seen_items: %v", err)
	}
	if rows != 1 {
		t.Fatalf("seen_items rows = %d, want 1", rows)
	}
}

func TestSeenItemsRepository_Claim_RollsBackWithSurroundingTransaction(t *testing.T) {
	db := newSeenItemsTestDB(t)
	repo := sqliteadapter.NewSeenItemsRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	insertArticleStub(t, db, "article-1", now)
	identity := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/x")

	errInjected := errors.New("injected downstream failure")
	err := uow.Do(context.Background(), func(ctx context.Context) error {
		if _, err := repo.Claim(ctx, identity, "article-1", "hash-1", "https://example.com/x", now, 10*time.Minute); err != nil {
			return err
		}
		return errInjected
	})
	if !errors.Is(err, errInjected) {
		t.Fatalf("uow.Do() error = %v, want errInjected", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM seen_items`).Scan(&count); err != nil {
		t.Fatalf("count seen_items: %v", err)
	}
	if count != 0 {
		t.Fatalf("seen_items rows = %d, want 0 (claim must roll back with the rest of the transaction)", count)
	}
}
