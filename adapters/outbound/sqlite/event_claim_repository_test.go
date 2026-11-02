package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var claimNow = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func TestEventClaimRepository_TryClaim_FirstWinsSecondLoses(t *testing.T) {
	repo := sqliteadapter.NewEventClaimRepository(newTestDB(t))
	ctx := context.Background()

	won, err := repo.TryClaim(ctx, "injury-report", "2026-09-13", "a1", json.RawMessage(`{"week":2}`), claimNow)
	if err != nil || !won {
		t.Fatalf("first TryClaim = %v, %v; want true, nil", won, err)
	}
	won, err = repo.TryClaim(ctx, "injury-report", "2026-09-13", "a2", nil, claimNow.Add(time.Minute))
	if err != nil || won {
		t.Fatalf("second TryClaim = %v, %v; want false, nil", won, err)
	}

	got, found, err := repo.Get(ctx, "injury-report", "2026-09-13")
	if err != nil || !found {
		t.Fatalf("Get = %v, %v", found, err)
	}
	if got.ArticleID != "a1" || got.State != out.EventClaimClaimed || string(got.Data) != `{"week":2}` || !got.CreatedAt.Equal(claimNow) {
		t.Fatalf("claim = %+v, want the first caller's row untouched", got)
	}
}

func TestEventClaimRepository_TryClaim_KeyIsPerHandler(t *testing.T) {
	repo := sqliteadapter.NewEventClaimRepository(newTestDB(t))
	ctx := context.Background()
	for _, h := range []string{"injury-report", "inactives"} {
		won, err := repo.TryClaim(ctx, h, "k", "a1", nil, claimNow)
		if err != nil || !won {
			t.Fatalf("TryClaim(%s) = %v, %v; want true", h, won, err)
		}
	}
}

func TestEventClaimRepository_TryClaim_Validation(t *testing.T) {
	repo := sqliteadapter.NewEventClaimRepository(newTestDB(t))
	ctx := context.Background()
	if _, err := repo.TryClaim(ctx, "", "k", "a", nil, claimNow); err == nil {
		t.Error("blank handler: want error")
	}
	if _, err := repo.TryClaim(ctx, "h", "", "a", nil, claimNow); err == nil {
		t.Error("blank key: want error")
	}
	if _, err := repo.TryClaim(ctx, "h", "k", "a", json.RawMessage(`{nope`), claimNow); err == nil {
		t.Error("invalid JSON: want error")
	}
	got, found, _ := repo.Get(ctx, "h", "k")
	if found {
		t.Errorf("a rejected claim left a row: %+v", got)
	}
}

func TestEventClaimRepository_TryClaim_EmptyDataReadsBackAsObject(t *testing.T) {
	repo := sqliteadapter.NewEventClaimRepository(newTestDB(t))
	ctx := context.Background()
	if _, err := repo.TryClaim(ctx, "h", "k", "a", nil, claimNow); err != nil {
		t.Fatal(err)
	}
	got, _, _ := repo.Get(ctx, "h", "k")
	if string(got.Data) != `{}` {
		t.Fatalf("Data = %q, want {}", got.Data)
	}
}

func TestEventClaimRepository_ConcurrentClaimsExactlyOneWins(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEventClaimRepository(db)

	const workers = 16
	var winners atomic.Int32
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			won, err := repo.TryClaim(context.Background(), "injury-report", "2026-09-13", "article", nil, claimNow)
			errs[i] = err
			if won {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("winners = %d, want exactly 1", got)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM event_claims`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("event_claims rows = %d, %v; want 1", rows, err)
	}
}

func TestEventClaimRepository_ClaimRollsBackWithUnitOfWork(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEventClaimRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	ctx := context.Background()
	boom := errors.New("boom")

	err := uow.Do(ctx, func(ctx context.Context) error {
		if won, err := repo.TryClaim(ctx, "h", "k", "a", nil, claimNow); err != nil || !won {
			t.Fatalf("TryClaim in tx = %v, %v", won, err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do error = %v, want boom", err)
	}
	if _, found, _ := repo.Get(ctx, "h", "k"); found {
		t.Fatal("claim survived a rolled-back unit of work")
	}
	if won, err := repo.TryClaim(ctx, "h", "k", "a", nil, claimNow); err != nil || !won {
		t.Fatalf("key not free after rollback: %v, %v", won, err)
	}
}

func TestEventClaimRepository_StateTransitions(t *testing.T) {
	repo := sqliteadapter.NewEventClaimRepository(newTestDB(t))
	ctx := context.Background()
	later := claimNow.Add(time.Hour)

	if err := repo.MarkPublished(ctx, "h", "missing", later); !errors.Is(err, out.ErrEventClaimNotFound) {
		t.Fatalf("MarkPublished(missing) = %v, want ErrEventClaimNotFound", err)
	}
	if err := repo.MarkFailed(ctx, "h", "missing", later); !errors.Is(err, out.ErrEventClaimNotFound) {
		t.Fatalf("MarkFailed(missing) = %v, want ErrEventClaimNotFound", err)
	}

	_, _ = repo.TryClaim(ctx, "h", "ok", "a1", nil, claimNow)
	_, _ = repo.TryClaim(ctx, "h", "bad", "a2", nil, claimNow)
	if err := repo.MarkPublished(ctx, "h", "ok", later); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkFailed(ctx, "h", "bad", later); err != nil {
		t.Fatal(err)
	}
	ok, _, _ := repo.Get(ctx, "h", "ok")
	bad, _, _ := repo.Get(ctx, "h", "bad")
	if ok.State != out.EventClaimPublished || bad.State != out.EventClaimFailed {
		t.Fatalf("states = %s, %s", ok.State, bad.State)
	}
	if !ok.UpdatedAt.Equal(later) || !ok.CreatedAt.Equal(claimNow) {
		t.Fatalf("timestamps = created %v updated %v", ok.CreatedAt, ok.UpdatedAt)
	}
}

func TestEventClaimRepository_Release(t *testing.T) {
	repo := sqliteadapter.NewEventClaimRepository(newTestDB(t))
	ctx := context.Background()

	if released, err := repo.Release(ctx, "h", "missing"); err != nil || released {
		t.Fatalf("Release(missing) = %v, %v; want false, nil", released, err)
	}

	_, _ = repo.TryClaim(ctx, "h", "failed", "a1", nil, claimNow)
	_ = repo.MarkFailed(ctx, "h", "failed", claimNow)
	if released, err := repo.Release(ctx, "h", "failed"); err != nil || !released {
		t.Fatalf("Release(failed) = %v, %v; want true", released, err)
	}
	if won, _ := repo.TryClaim(ctx, "h", "failed", "a2", nil, claimNow); !won {
		t.Fatal("a released key must be claimable again")
	}

	_, _ = repo.TryClaim(ctx, "h", "done", "a3", nil, claimNow)
	_ = repo.MarkPublished(ctx, "h", "done", claimNow)
	if released, err := repo.Release(ctx, "h", "done"); !errors.Is(err, out.ErrEventClaimPublished) || released {
		t.Fatalf("Release(published) = %v, %v; want ErrEventClaimPublished", released, err)
	}
	if _, found, _ := repo.Get(ctx, "h", "done"); !found {
		t.Fatal("a published claim must survive Release")
	}
}

func TestEventClaimRepository_List(t *testing.T) {
	repo := sqliteadapter.NewEventClaimRepository(newTestDB(t))
	ctx := context.Background()
	_, _ = repo.TryClaim(ctx, "injury-report", "d1", "a1", nil, claimNow)
	_, _ = repo.TryClaim(ctx, "inactives", "g1", "a2", nil, claimNow)
	_, _ = repo.TryClaim(ctx, "injury-report", "d2", "a3", nil, claimNow)
	_ = repo.MarkPublished(ctx, "injury-report", "d1", claimNow)

	keys := func(f out.EventClaimFilter) []string {
		t.Helper()
		claims, err := repo.List(ctx, f)
		if err != nil {
			t.Fatalf("List(%+v): %v", f, err)
		}
		var ks []string
		for _, c := range claims {
			ks = append(ks, c.Key)
		}
		return ks
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if got := keys(out.EventClaimFilter{}); !eq(got, []string{"d2", "g1", "d1"}) {
		t.Errorf("all = %v, want newest first [d2 g1 d1]", got)
	}
	if got := keys(out.EventClaimFilter{Handler: "injury-report"}); !eq(got, []string{"d2", "d1"}) {
		t.Errorf("by handler = %v", got)
	}
	if got := keys(out.EventClaimFilter{State: out.EventClaimPublished}); !eq(got, []string{"d1"}) {
		t.Errorf("by state = %v", got)
	}
	if got := keys(out.EventClaimFilter{Limit: 1}); !eq(got, []string{"d2"}) {
		t.Errorf("limit = %v", got)
	}
	if _, err := repo.List(ctx, out.EventClaimFilter{State: "bogus"}); err == nil {
		t.Error("unknown state: want error")
	}
}

func TestEventClaimRepository_ReconcileWithOutbox(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEventClaimRepository(db)
	ctx := context.Background()

	seed := []struct{ article, status string }{
		{"delivered", "delivered"},
		{"dead", "dead"},
		{"pending", "pending"},
		{"mixed", "delivered"}, // second row below stays pending
	}
	for _, s := range seed {
		insertArticleStub(t, db, s.article, claimNow)
		if _, err := db.Exec(`INSERT INTO publication_outbox (article_id, destination, status, available_at) VALUES (?, 'telegram', ?, CURRENT_TIMESTAMP)`, s.article, s.status); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.TryClaim(ctx, "h", s.article, s.article, nil, claimNow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO publication_outbox (article_id, destination, status, available_at) VALUES ('mixed', 'bluesky', 'retryable', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	// A claim with no outbox row at all (handler claimed, enqueue never happened) is left alone.
	_, _ = repo.TryClaim(ctx, "h", "no-outbox", "ghost", nil, claimNow)

	later := claimNow.Add(time.Hour)
	published, failed, err := repo.ReconcileWithOutbox(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 || failed != 1 {
		t.Fatalf("published, failed = %d, %d; want 1, 1", published, failed)
	}
	want := map[string]out.EventClaimState{
		"delivered": out.EventClaimPublished,
		"dead":      out.EventClaimFailed,
		"pending":   out.EventClaimClaimed,
		"mixed":     out.EventClaimClaimed,
		"no-outbox": out.EventClaimClaimed,
	}
	for key, state := range want {
		c, _, _ := repo.Get(ctx, "h", key)
		if c.State != state {
			t.Errorf("claim %s state = %s, want %s", key, c.State, state)
		}
	}

	published, failed, err = repo.ReconcileWithOutbox(ctx, later)
	if err != nil || published != 0 || failed != 0 {
		t.Fatalf("second reconcile = %d, %d, %v; want 0, 0, nil", published, failed, err)
	}
}
