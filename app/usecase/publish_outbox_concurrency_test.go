package usecase_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

// TestConcurrentDispatchers_NoDoubleSend runs N PublishOutbox instances —
// simulating N concurrent scheduler dispatchers, or one dispatcher racing
// its own overlapping tick — against the same real SQLite database and a
// shared fake Telegram server, and asserts every article was sent to
// Telegram exactly once. ClaimBatch's own doc comment already documents
// why this is safe at the SQL level (single-connection serialization,
// see adapters/outbound/sqlite/outbox_repository.go); this test
// exercises that guarantee through the full PublishOutbox path, including
// the new sent_unconfirmed evidence write, under -race.
func TestConcurrentDispatchers_NoDoubleSend(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	const articleCount = 20
	for i := 0; i < articleCount; i++ {
		seedRealPendingArticle(t, db, fmt.Sprintf("a%d", i), fmt.Sprintf("https://example.com/%d", i), now)
	}

	const dispatchers = 5
	var wg sync.WaitGroup
	for d := 0; d < dispatchers; d++ {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			uc, _ := newRealPublishOutbox(db, srv, workerID, now)
			for i := 0; i < 3; i++ { // a few ticks each, overlapping in time
				_, _ = uc.Run(context.Background())
			}
		}(fmt.Sprintf("worker-%d", d))
	}
	wg.Wait()

	if got := srv.Calls(); got != articleCount {
		t.Fatalf("srv.Calls() = %d, want exactly %d — every article sent exactly once despite %d concurrent dispatchers", got, articleCount, dispatchers)
	}

	outbox := sqliteadapter.NewOutboxRepository(db)
	counts, err := outbox.StatusCounts(context.Background(), now)
	if err != nil {
		t.Fatalf("StatusCounts() error = %v", err)
	}
	if counts[publishing.OutboxDelivered].Count != articleCount {
		t.Fatalf("delivered count = %d, want %d", counts[publishing.OutboxDelivered].Count, articleCount)
	}
}

// TestExpiredLeaseReclaimedByAnotherWorker_StillExactlyOneSend is
// deliberately NOT a concurrency test — it runs on one goroutine and
// forces the interleaving by hand, which is why it can assert an exact
// call count instead of a range. It is a simpler restatement, from this
// file's perspective, of the same invariant
// TestFaultInjection_P1_CrashBeforeCallingTelegram_LeasedItemSafelyReclaimed
// covers: a lease that has aged past staleAfter is reclaimable, and
// reclaiming it produces exactly one send because nothing was ever sent
// under the original lease. The actual concurrency proof — real goroutines,
// run under -race — is TestConcurrentDispatchers_NoDoubleSend above.
func TestExpiredLeaseReclaimedByAnotherWorker_StillExactlyOneSend(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	outbox := sqliteadapter.NewOutboxRepository(db)
	// worker-1 claims but "hangs" (simulated: just holds the lease by
	// never calling Run); worker-2 then runs with a clock two minutes
	// later, so the lease reads as stale and is reclaimed. Sequential and
	// deterministic — no timing race is being exercised here.
	if _, err := outbox.ClaimBatch(context.Background(), "worker-1", 10, time.Minute, now); err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}

	uc2, _ := newRealPublishOutbox(db, srv, "worker-2", now.Add(2*time.Minute))
	if _, err := uc2.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want 1", srv.Calls())
	}
}
