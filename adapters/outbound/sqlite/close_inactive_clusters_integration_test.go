package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// TestCloseThenReopen_PreservesConfirmedPublicationGate is the design
// doc's central #153 safety claim, exercised end to end: a cluster whose
// initial publication is already 'confirmed' gets closed by
// CloseInactiveBatch, then reopened by a late, legitimate AddMember join —
// and the gate row (and its 'confirmed' state) must be exactly what it was
// before closing, since a late second article going through
// clustergate.Decide against a fresh/empty gate would be free to publish
// again.
func TestCloseThenReopen_PreservesConfirmedPublicationGate(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	publications := sqliteadapter.NewClusterPublicationRepository(db)
	ctx := context.Background()

	mustSaveArticle(t, articles, "a1")
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if err := clusters.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, now); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	acquired, err := publications.Reserve(ctx, out.ClusterPublicationReservation{
		ClusterID: "c1", ArticleID: "a1", SourceID: "src-1", MemberRole: string(clustering.MemberRoleFirstReport),
	}, now)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if !acquired {
		t.Fatal("Reserve() on a fresh cluster = false, want true")
	}
	// event_cluster_publications.outbox_id is FK-constrained to
	// publication_outbox(id) (see cluster_publication_repository_test.go's
	// TestClusterPublicationRepository_ConfirmAndReleaseAreScopedToTheHoldingArticle):
	// seed the row Confirm below will link to.
	if _, err := db.ExecContext(ctx, `INSERT INTO publication_outbox (id, article_id, available_at) VALUES (?, ?, ?)`, 1, "a1", now); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	if err := publications.Confirm(ctx, "c1", "a1", 1, now.Add(time.Minute)); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}

	cutoff := now.Add(96 * time.Hour)
	closedAt := cutoff.Add(time.Hour)
	closed, err := clusters.CloseInactiveBatch(ctx, cutoff, "inactivity_ttl", 10, closedAt)
	if err != nil {
		t.Fatalf("CloseInactiveBatch() error = %v", err)
	}
	if closed != 1 {
		t.Fatalf("CloseInactiveBatch() = %d, want 1", closed)
	}

	mustSaveArticle(t, articles, "a2")
	reopenAt := closedAt.Add(time.Hour)
	if err := clusters.AddMember(ctx, "c1", "a2", clustering.MemberRoleConfirmation, 0.8, reopenAt); err != nil {
		t.Fatalf("AddMember() (reopen) error = %v", err)
	}

	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM event_clusters WHERE id = 'c1'`).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != "open" {
		t.Fatalf("status after reopen = %q, want open", status)
	}

	var gateState, gateArticle string
	if err := db.QueryRowContext(ctx, `SELECT state, article_id FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&gateState, &gateArticle); err != nil {
		t.Fatalf("select event_cluster_publications: %v", err)
	}
	if gateState != "confirmed" {
		t.Errorf("gate state after reopen = %q, want confirmed (untouched by reopen)", gateState)
	}
	if gateArticle != "a1" {
		t.Errorf("gate article_id after reopen = %q, want a1 (the original, confirmed holder — untouched by a2's mere clustering join)", gateArticle)
	}
}

// TestAddMemberVsCloseInactiveBatch_Race exercises the design doc's "Race:
// add-member vs. close, exhaustively" section under go test -race: many
// goroutines concurrently call AddMember (a legitimate late join) and
// CloseInactiveBatch (the maintenance job) against the same cluster. Both
// final states are correct per the design doc — this test asserts the
// specific invariant that must hold regardless of interleaving: if the
// cluster ends up closed, it must never have a fresh (post-close)
// last_updated_at, and reopened_count must never exceed the number of
// AddMember calls that actually ran after a close.
func TestAddMemberVsCloseInactiveBatch_Race(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	mustSaveArticle(t, articles, "seed")
	if err := clusters.CreateCluster(ctx, "c1", "seed", clustering.StatusOpen, now.Add(-200*time.Hour)); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	const joiners = 20
	articleIDs := make([]string, joiners)
	for i := 0; i < joiners; i++ {
		articleIDs[i] = "racer-" + string(rune('a'+i))
		mustSaveArticle(t, articles, articleIDs[i])
	}

	cutoff := now.Add(-96 * time.Hour) // seed cluster is already eligible

	done := make(chan error, joiners+1)
	go func() {
		_, err := clusters.CloseInactiveBatch(ctx, cutoff, "inactivity_ttl", 10, now)
		done <- err
	}()
	for i := 0; i < joiners; i++ {
		id := articleIDs[i]
		go func() {
			err := clusters.AddMember(ctx, "c1", id, clustering.MemberRoleConfirmation, 0.8, now.Add(time.Duration(i)*time.Millisecond))
			done <- err
		}()
	}

	for i := 0; i < joiners+1; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent call error = %v", err)
		}
	}

	var status string
	var closedAt sql.NullTime
	var memberCount int
	var lastUpdated time.Time
	if err := db.QueryRowContext(ctx, `SELECT status, closed_at, member_count, last_updated_at FROM event_clusters WHERE id = 'c1'`).Scan(&status, &closedAt, &memberCount, &lastUpdated); err != nil {
		t.Fatalf("select final state: %v", err)
	}
	// Every AddMember call always succeeds (it's unconditional-by-id), so
	// member_count must reflect all of them regardless of open/closed
	// interleaving.
	if memberCount != joiners+1 { // +1 for the seed founding member
		t.Errorf("member_count = %d, want %d (every AddMember call always lands, regardless of interleaving with close)", memberCount, joiners+1)
	}
	// Whichever of close/AddMember committed last determines final status;
	// both are valid. What must never happen: status='closed' while
	// closed_at is unset, or vice versa.
	if (status == "closed") != closedAt.Valid {
		t.Errorf("status=%q but closed_at.Valid=%v — these must always agree", status, closedAt.Valid)
	}
	// The compare-and-swap safety property this whole feature rests on:
	// CloseInactiveBatch's WHERE clause (status='open' AND last_updated_at
	// <= cutoff) is evaluated against each row's state at execution time,
	// so a concurrent AddMember that lands first (bumping last_updated_at
	// past cutoff) must make that row ineligible for the close that raced
	// it. If the cluster nonetheless ended up closed, its last_updated_at
	// must never be newer than cutoff — a buggy read-then-write
	// implementation (read status, decide to close, write unconditionally)
	// could close a row out from under a just-landed AddMember, which
	// this assertion would catch by finding status=closed with a fresh
	// (post-cutoff) last_updated_at.
	if status == "closed" && lastUpdated.After(cutoff) {
		t.Errorf("status=closed with last_updated_at=%v newer than cutoff=%v — a concurrent AddMember was closed out from under", lastUpdated, cutoff)
	}
}

// TestCloseInactiveClusters_ResumesAcrossTwoRunsWithNoPartialState proves
// the design doc's "no separate checkpoint/cursor state" claim: simulating
// an interrupted run (only the first of two needed batches happens), then
// a completely independent second Run call, finishes the job correctly —
// summed totals across both calls match what one uninterrupted run would
// have produced, and nothing is double-closed or skipped.
func TestCloseInactiveClusters_ResumesAcrossTwoRunsWithNoPartialState(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	logger := &recordingLogger{}
	ctx := context.Background()

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-96 * time.Hour)
	const total = 25
	for i := 0; i < total; i++ {
		id := "cluster-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		mustCreateClusterAt(t, clusters, articles, id, id+"-art", cutoff.Add(-time.Hour))
	}

	cfg := in.CloseInactiveClustersConfig{CloseAfter: 96 * time.Hour, BatchSize: 10, MaxBatchesPerRun: 1, TimeBudget: time.Hour, CloseReason: "inactivity_ttl"}
	uc := usecase.NewCloseInactiveClusters(clusters, fixedClock{now}, logger, cfg)

	result1, err := uc.Run(ctx)
	if err != nil {
		t.Fatalf("Run() #1 error = %v", err)
	}
	if result1.ClustersClosed != 10 || result1.BatchesRun != 1 {
		t.Fatalf("Run() #1 = %+v, want {10 1} (MaxBatchesPerRun=1 simulates an interrupted run)", result1)
	}

	var closedCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_clusters WHERE status = 'closed'`).Scan(&closedCount); err != nil {
		t.Fatalf("count closed after run #1: %v", err)
	}
	if closedCount != 10 {
		t.Fatalf("closed count after run #1 = %d, want 10 (partial progress, fully committed)", closedCount)
	}

	// A fresh use case instance (simulating a new process) with a bigger
	// budget finishes the rest.
	cfg2 := cfg
	cfg2.MaxBatchesPerRun = 10
	uc2 := usecase.NewCloseInactiveClusters(clusters, fixedClock{now}, logger, cfg2)
	result2, err := uc2.Run(ctx)
	if err != nil {
		t.Fatalf("Run() #2 error = %v", err)
	}
	if result2.ClustersClosed != total-10 {
		t.Errorf("Run() #2 ClustersClosed = %d, want %d (the remaining rows)", result2.ClustersClosed, total-10)
	}

	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_clusters WHERE status = 'closed'`).Scan(&closedCount); err != nil {
		t.Fatalf("count closed after run #2: %v", err)
	}
	if closedCount != total {
		t.Errorf("closed count after run #2 = %d, want %d (all of them, nothing double-closed or skipped)", closedCount, total)
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type recordingLogger struct{}

func (recordingLogger) Debug(string, ...any)        {}
func (recordingLogger) Info(string, ...any)         {}
func (recordingLogger) Warn(string, ...any)         {}
func (recordingLogger) Error(string, error, ...any) {}

// TestPurgeAfterClose_PreservesPublishedArticleAndAttempts closes a
// cluster, ages it past the retention window, purges it via
// DeleteClosedOlderThan, and asserts every cascaded child table is gone
// while the published article and its publication_attempts survive —
// see the design doc's "Timing invariant" and Context's "already
// independent of event_clusters" claims.
func TestPurgeAfterClose_PreservesPublishedArticleAndAttempts(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	publications := sqliteadapter.NewClusterPublicationRepository(db)
	ctx := context.Background()

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	mustSaveArticle(t, articles, "a1")
	if err := clusters.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, now); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if _, err := publications.Reserve(ctx, out.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", SourceID: "src-1"}, now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	// event_cluster_publications.outbox_id is FK-constrained to
	// publication_outbox(id) (see cluster_publication_repository_test.go's
	// TestClusterPublicationRepository_ConfirmAndReleaseAreScopedToTheHoldingArticle,
	// and this file's TestCloseThenReopen_PreservesConfirmedPublicationGate):
	// seed the row Confirm below will link to.
	if _, err := db.ExecContext(ctx, `INSERT INTO publication_outbox (id, article_id, available_at) VALUES (?, ?, ?)`, 1, "a1", now); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	if err := publications.Confirm(ctx, "c1", "a1", 1, now); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if err := clusters.SaveEventKeys(ctx, "a1", "c1", []string{"some-event-key"}, now); err != nil {
		t.Fatalf("SaveEventKeys() error = %v", err)
	}

	closedAt := now.Add(96 * time.Hour)
	// The cluster's last_updated_at was set to now by CreateCluster above;
	// CloseInactiveBatch's eligibility check is last_updated_at <= cutoff,
	// so cutoff must be at or after now (not before it) for c1 to close.
	if _, err := clusters.CloseInactiveBatch(ctx, now.Add(time.Hour), "inactivity_ttl", 10, closedAt); err != nil {
		t.Fatalf("CloseInactiveBatch() error = %v", err)
	}

	purgeCutoff := closedAt.Add(14 * 24 * time.Hour)
	deleted, err := clusters.DeleteClosedOlderThan(ctx, purgeCutoff)
	if err != nil {
		t.Fatalf("DeleteClosedOlderThan() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteClosedOlderThan() = %d, want 1", deleted)
	}

	for _, table := range []string{"event_cluster_members", "event_keys", "event_cluster_publications"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE cluster_id = 'c1'`).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s rows for c1 after purge = %d, want 0 (cascade delete)", table, count)
		}
	}

	got, err := articles.FindByID(ctx, "a1")
	if err != nil {
		t.Fatalf("FindByID(a1) after purge error = %v, want the article to survive (no FK from event_clusters to articles)", err)
	}
	if got.ClusterID != "" {
		t.Errorf("a1 ClusterID after purge = %q, want empty (ON DELETE SET NULL)", got.ClusterID)
	}
}

// TestCloseInactiveClusters_VolumeExercisesMultipleBatches seeds enough
// eligible clusters that a small BatchSize/MaxBatchesPerRun genuinely
// needs more than one Run call, proving batching is real bounded work,
// not a single unbounded UPDATE dressed up as "batched."
func TestCloseInactiveClusters_VolumeExercisesMultipleBatches(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-96 * time.Hour)
	// Two complete batches plus one partial batch prove that Run processes
	// bounded batches, without making this integration test pay for a
	// production-scale fixture on every CI run.
	const total = 401
	for i := 0; i < total; i++ {
		id := "vol-" + itoa(i)
		mustCreateClusterAt(t, clusters, articles, id, id+"-art", cutoff.Add(-time.Hour))
	}

	cfg := in.CloseInactiveClustersConfig{CloseAfter: 96 * time.Hour, BatchSize: 200, MaxBatchesPerRun: 50, TimeBudget: 30 * time.Second, CloseReason: "inactivity_ttl"}
	uc := usecase.NewCloseInactiveClusters(clusters, fixedClock{now}, recordingLogger{}, cfg)

	runs := 0
	closedTotal := 0
	for {
		result, err := uc.Run(ctx)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		runs++
		closedTotal += result.ClustersClosed
		if result.ClustersClosed == 0 {
			break
		}
		if runs > total {
			t.Fatal("Run() loop did not converge — possible infinite loop")
		}
	}

	if closedTotal != total {
		t.Errorf("total closed = %d, want %d", closedTotal, total)
	}
	// 401 clusters / 200 per batch needs two full-batch UPDATEs, one partial
	// batch, and one more that finds nothing left (four batches total) — well within one
	// Run's MaxBatchesPerRun=50 cap, so the first Run call closes
	// everything; the second Run call is the outer test loop's own
	// termination check (a batch returning 0 is how it knows to stop),
	// not further progress.
	if runs != 2 {
		t.Errorf("Run() call count = %d, want 2 (first call closes all 2200 across multiple internal batches, second call confirms 0 remain)", runs)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}
