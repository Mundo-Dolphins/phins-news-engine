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

// seedClusterAndArticles inserts one open cluster and n articles
// (ids "a1".."aN") satisfying event_cluster_publications' foreign keys,
// against db.
func seedClusterAndArticles(t testing.TB, db *sql.DB, clusterID string, articleIDs []string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES (?, ?, ?, 'open')`, clusterID, now, now); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	for _, id := range articleIDs {
		if _, err := db.ExecContext(ctx, `INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES (?, ?, ?, ?, 'clustered')`, id, "https://x/"+id, "https://x/"+id, now); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}
}

func TestClusterPublicationRepository_ReserveFreshClusterSucceeds(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticles(t, db, "c1", []string{"a1"}, now)
	repo := sqliteadapter.NewClusterPublicationRepository(db)

	acquired, err := repo.Reserve(context.Background(), appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", MemberRole: "first_report"}, now)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true on a fresh cluster")
	}
}

func TestClusterPublicationRepository_SecondReserveOnActiveClusterFails(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticles(t, db, "c1", []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewClusterPublicationRepository(db)
	ctx := context.Background()

	first, err := repo.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", MemberRole: "first_report"}, now)
	if err != nil || !first {
		t.Fatalf("first Reserve() = %v, %v, want true, nil", first, err)
	}
	second, err := repo.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a2", MemberRole: "confirmation"}, now)
	if err != nil {
		t.Fatalf("second Reserve() error = %v", err)
	}
	if second {
		t.Error("second acquired = true, want false: cluster already has an active reservation")
	}

	var articleID string
	if err := db.QueryRowContext(ctx, `SELECT article_id FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&articleID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if articleID != "a1" {
		t.Errorf("holder = %q, want a1: the losing Reserve must not overwrite the row", articleID)
	}
}

func TestClusterPublicationRepository_ReserveAfterReleaseSucceeds(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticles(t, db, "c1", []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewClusterPublicationRepository(db)
	ctx := context.Background()

	if _, err := repo.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", MemberRole: "first_report"}, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := repo.Release(ctx, "c1", "a1", now); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	acquired, err := repo.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a2", MemberRole: "first_report"}, now)
	if err != nil {
		t.Fatalf("re-Reserve() error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true: a released reservation must be re-claimable")
	}

	var state, articleID string
	if err := db.QueryRowContext(ctx, `SELECT state, article_id FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state, &articleID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if state != "reserved" || articleID != "a2" {
		t.Errorf("row = state=%q article_id=%q, want state=reserved article_id=a2", state, articleID)
	}
}

func TestClusterPublicationRepository_ConfirmAndReleaseAreScopedToTheHoldingArticle(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticles(t, db, "c1", []string{"a1", "a2"}, now)
	repo := sqliteadapter.NewClusterPublicationRepository(db)
	ctx := context.Background()

	if _, err := repo.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", MemberRole: "first_report"}, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	// event_cluster_publications.outbox_id is FK-constrained to
	// publication_outbox(id): seed the row Confirm below will link to.
	if _, err := db.ExecContext(ctx, `INSERT INTO publication_outbox (id, article_id, available_at) VALUES (?, ?, ?)`, 42, "a1", now); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}

	// a2 never held the reservation: Confirm/Release for it must be a no-op.
	if err := repo.Confirm(ctx, "c1", "a2", 99, now); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if err := repo.Release(ctx, "c1", "a2", now); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state); err != nil {
		t.Fatalf("query: %v", err)
	}
	if state != "reserved" {
		t.Errorf("state = %q, want reserved: Confirm/Release for a non-holding article must not change the row", state)
	}

	// a1 (the real holder) can confirm.
	if err := repo.Confirm(ctx, "c1", "a1", 42, now); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	var outboxID int64
	if err := db.QueryRowContext(ctx, `SELECT state, outbox_id FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state, &outboxID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if state != "confirmed" || outboxID != 42 {
		t.Errorf("state=%q outbox_id=%d, want confirmed/42", state, outboxID)
	}
}

// TestClusterPublicationRepository_ReserveInsideRolledBackTransactionLeavesNoRow
// is the "fallo antes de crear outbox" case: a Reserve that succeeds inside
// a UnitOfWork.Do transaction that later fails must leave no trace once the
// transaction rolls back — proving the reservation is genuinely
// transactional, not a separately-committed side effect.
func TestClusterPublicationRepository_ReserveInsideRolledBackTransactionLeavesNoRow(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	seedClusterAndArticles(t, db, "c1", []string{"a1", "a2"}, now)
	uow := sqliteadapter.NewUnitOfWork(db)
	repo := sqliteadapter.NewClusterPublicationRepository(db)
	forcedErr := errors.New("forced failure after reserve")

	err := uow.Do(context.Background(), func(txCtx context.Context) error {
		acquired, err := repo.Reserve(txCtx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", MemberRole: "first_report"}, now)
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

	acquired, err := repo.Reserve(context.Background(), appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a2", MemberRole: "first_report"}, now)
	if err != nil {
		t.Fatalf("post-rollback Reserve() error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true: the earlier failed transaction must not have left a reservation behind")
	}
}

// TestClusterPublicationRepository_ConcurrentReserveOnlyOneWinner is the
// "carrera de dos workers" case at the adapter level: two independent
// *sql.DB connections (simulating two worker processes) racing Reserve for
// the same cluster must produce exactly one true and one false — proven
// under go test -race.
func TestClusterPublicationRepository_ConcurrentReserveOnlyOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "race.db")
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
	seedClusterAndArticles(t, db1, "cluster-race", []string{"a1", "a2"}, now)

	repo1 := sqliteadapter.NewClusterPublicationRepository(db1)
	repo2 := sqliteadapter.NewClusterPublicationRepository(db2)

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0], errs[0] = repo1.Reserve(context.Background(), appout.ClusterPublicationReservation{ClusterID: "cluster-race", ArticleID: "a1", MemberRole: "first_report"}, now)
	}()
	go func() {
		defer wg.Done()
		results[1], errs[1] = repo2.Reserve(context.Background(), appout.ClusterPublicationReservation{ClusterID: "cluster-race", ArticleID: "a2", MemberRole: "confirmation"}, now)
	}()
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("Reserve() errors = %v, %v, want both nil", errs[0], errs[1])
	}
	if results[0] == results[1] {
		t.Fatalf("results = %v, %v, want exactly one true and one false", results[0], results[1])
	}
}
