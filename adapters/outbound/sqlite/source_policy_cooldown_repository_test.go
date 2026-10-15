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
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// seedClusterAndArticlesForCooldown mirrors seedClusterAndArticles (this
// package's existing helper for event_cluster_publications tests) —
// source_policy_cooldowns has the same articles(id) foreign key, but no
// dependency on event_clusters, so only articles are seeded here.
func seedClusterAndArticlesForCooldown(t testing.TB, db *sql.DB, articleIDs []string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, id := range articleIDs {
		if _, err := db.ExecContext(ctx, `INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES (?, ?, ?, ?, 'clustered')`, id, "https://x/"+id, "https://x/"+id, now); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}
}

func TestSourcePolicyCooldownRepository_ReserveFreshWindowSucceeds(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1"}, now)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)

	acquired, err := repo.Reserve(context.Background(), appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true on a fresh window")
	}
}

func TestSourcePolicyCooldownRepository_SecondReserveWithinActiveWindowFails(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	ctx := context.Background()

	first, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now)
	if err != nil || !first {
		t.Fatalf("first Reserve() = %v, %v, want true, nil", first, err)
	}
	second, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a2"}, 20*time.Minute, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("second Reserve() error = %v", err)
	}
	if second {
		t.Error("second acquired = true, want false: an active window is held")
	}

	var articleID string
	if err := db.QueryRowContext(ctx, `SELECT article_id FROM source_policy_cooldowns WHERE source_id = 's1' AND cluster_id = 'c1'`).Scan(&articleID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if articleID != "a1" {
		t.Errorf("holder = %q, want a1: the losing Reserve must not overwrite the row", articleID)
	}
}

func TestSourcePolicyCooldownRepository_ReserveAfterNaturalExpirySucceeds(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	ctx := context.Background()

	if _, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	// This is the orphan-recovery case: Release is never called (crash
	// or a stuck retry), but 20 minutes later the window has naturally
	// elapsed and a fresh candidate can still claim it.
	acquired, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a2"}, 20*time.Minute, now.Add(20*time.Minute))
	if err != nil {
		t.Fatalf("post-expiry Reserve() error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true: an elapsed cooldown_until must be re-claimable without an explicit Release")
	}
}

func TestSourcePolicyCooldownRepository_ReserveAfterReleaseSucceeds(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	ctx := context.Background()

	if _, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	affected, err := repo.Release(ctx, "s1", "c1", "a1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if !affected {
		t.Error("Release() affected = false, want true: a1 holds the reservation")
	}

	acquired, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a2"}, 20*time.Minute, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("re-Reserve() error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true: a released reservation must be immediately re-claimable, before natural expiry")
	}
}

func TestSourcePolicyCooldownRepository_ConfirmAndReleaseAreScopedToTheHoldingArticle(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	ctx := context.Background()

	if _, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO publication_outbox (id, article_id, available_at) VALUES (?, ?, ?)`, 42, "a1", now); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}

	// a2 never held the reservation: Confirm/Release for it must be a no-op.
	affected, err := repo.Confirm(ctx, "s1", "c1", "a2", 99, now)
	if err != nil {
		t.Fatalf("Confirm(a2) error = %v", err)
	}
	if affected {
		t.Error("Confirm(a2) affected = true, want false: a2 does not hold the reservation")
	}
	affected, err = repo.Release(ctx, "s1", "c1", "a2", now)
	if err != nil {
		t.Fatalf("Release(a2) error = %v", err)
	}
	if affected {
		t.Error("Release(a2) affected = true, want false: a2 does not hold the reservation")
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM source_policy_cooldowns WHERE source_id = 's1' AND cluster_id = 'c1'`).Scan(&state); err != nil {
		t.Fatalf("query: %v", err)
	}
	if state != "reserved" {
		t.Errorf("state = %q, want reserved: Confirm/Release for a non-holding article must not change the row", state)
	}

	affected, err = repo.Confirm(ctx, "s1", "c1", "a1", 42, now)
	if err != nil {
		t.Fatalf("Confirm(a1) error = %v", err)
	}
	if !affected {
		t.Error("Confirm(a1) affected = false, want true: a1 holds the reservation")
	}
	var outboxID int64
	if err := db.QueryRowContext(ctx, `SELECT state, outbox_id FROM source_policy_cooldowns WHERE source_id = 's1' AND cluster_id = 'c1'`).Scan(&state, &outboxID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if state != "confirmed" || outboxID != 42 {
		t.Errorf("state=%q outbox_id=%d, want confirmed/42", state, outboxID)
	}
}

func TestSourcePolicyCooldownRepository_ForceReserveAlwaysWins(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	ctx := context.Background()

	// a1 holds an active (far from expired) window.
	if _, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	// A correction (a2) force-reserves over it despite the active window.
	if err := repo.ForceReserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a2", DecisionCode: "publish_substantial_update"}, 20*time.Minute, now.Add(time.Minute)); err != nil {
		t.Fatalf("ForceReserve() error = %v", err)
	}

	var state, articleID string
	if err := db.QueryRowContext(ctx, `SELECT state, article_id FROM source_policy_cooldowns WHERE source_id = 's1' AND cluster_id = 'c1'`).Scan(&state, &articleID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if state != "reserved" || articleID != "a2" {
		t.Errorf("row = state=%q article_id=%q, want state=reserved article_id=a2: ForceReserve must overwrite an active window unconditionally", state, articleID)
	}
}

func TestSourcePolicyCooldownRepository_ActiveCooldownUntilIgnoresReleasedRows(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1"}, now)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	ctx := context.Background()

	if _, _, err := repo.ActiveCooldownUntil(ctx, "s1", "c1"); err != nil {
		t.Fatalf("ActiveCooldownUntil() on empty table error = %v", err)
	}
	if _, ok, err := repo.ActiveCooldownUntil(ctx, "s1", "c1"); err != nil || ok {
		t.Fatalf("ActiveCooldownUntil() on empty table = (_, %v, %v), want ok=false", ok, err)
	}

	if _, err := repo.Reserve(ctx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	until, ok, err := repo.ActiveCooldownUntil(ctx, "s1", "c1")
	if err != nil || !ok {
		t.Fatalf("ActiveCooldownUntil() after reserve = (_, %v, %v), want ok=true", ok, err)
	}
	if !until.Equal(now.Add(20 * time.Minute)) {
		t.Errorf("cooldown_until = %v, want %v", until, now.Add(20*time.Minute))
	}

	if _, err := repo.Release(ctx, "s1", "c1", "a1", now.Add(time.Minute)); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, ok, err := repo.ActiveCooldownUntil(ctx, "s1", "c1"); err != nil || ok {
		t.Fatalf("ActiveCooldownUntil() after release = (_, %v, %v), want ok=false: a released row must never report an active cooldown", ok, err)
	}
}

// TestSourcePolicyCooldownRepository_ReserveInsideRolledBackTransactionLeavesNoRow
// mirrors TestClusterPublicationRepository_ReserveInsideRolledBackTransactionLeavesNoRow.
func TestSourcePolicyCooldownRepository_ReserveInsideRolledBackTransactionLeavesNoRow(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db, []string{"a1", "a2"}, now)
	uow := sqliteadapter.NewUnitOfWork(db)
	repo := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	forcedErr := errors.New("forced failure after reserve")

	err := uow.Do(context.Background(), func(txCtx context.Context) error {
		acquired, err := repo.Reserve(txCtx, appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a1"}, 20*time.Minute, now)
		if err != nil {
			return err
		}
		if !acquired {
			t.Fatal("expected to acquire the reservation before the forced failure")
		}
		return forcedErr
	})
	if !errors.Is(err, forcedErr) {
		t.Fatalf("uow.Do() error = %v, want wrapped forcedErr", err)
	}

	acquired, err := repo.Reserve(context.Background(), appout.SourcePolicyCooldownReservation{SourceID: "s1", ClusterID: "c1", ArticleID: "a2"}, 20*time.Minute, now)
	if err != nil {
		t.Fatalf("post-rollback Reserve() error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true: the earlier failed transaction must not have left a reservation behind")
	}
}

// TestSourcePolicyCooldownRepository_ConcurrentReserveOnlyOneWinner mirrors
// TestClusterPublicationRepository_ConcurrentReserveOnlyOneWinner — two
// independent *sql.DB connections racing Reserve for the same
// (source_id, cluster_id), proven under go test -race.
func TestSourcePolicyCooldownRepository_ConcurrentReserveOnlyOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cooldown-race.db")
	copyMigratedTestDB(t, path)
	db1, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() db1 error = %v", err)
	}
	defer db1.Close()
	db2, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() db2 error = %v", err)
	}
	defer db2.Close()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticlesForCooldown(t, db1, []string{"a1", "a2"}, now)

	repo1 := sqliteadapter.NewSourcePolicyCooldownRepository(db1)
	repo2 := sqliteadapter.NewSourcePolicyCooldownRepository(db2)

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0], errs[0] = repo1.Reserve(context.Background(), appout.SourcePolicyCooldownReservation{SourceID: "s-race", ClusterID: "c-race", ArticleID: "a1"}, 20*time.Minute, now)
	}()
	go func() {
		defer wg.Done()
		results[1], errs[1] = repo2.Reserve(context.Background(), appout.SourcePolicyCooldownReservation{SourceID: "s-race", ClusterID: "c-race", ArticleID: "a2"}, 20*time.Minute, now)
	}()
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("Reserve() errors = %v, %v, want both nil", errs[0], errs[1])
	}
	if results[0] == results[1] {
		t.Fatalf("results = %v, %v, want exactly one true and one false", results[0], results[1])
	}
}
