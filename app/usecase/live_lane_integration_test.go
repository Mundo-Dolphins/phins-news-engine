package usecase_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	telegramadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/telegram"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// seedRealLaneArticle is seedRealPendingArticle (see
// publish_outbox_fault_injection_test.go) plus lane control — it enqueues
// via EnqueueLane instead of Enqueue so this test can put items in both
// lanes against the real SQLite outbox. mustID, used below, is already
// defined elsewhere in this test package (publish_outbox_fault_injection_test.go).
func seedRealLaneArticle(t *testing.T, db *sql.DB, id, canonicalURL string, now time.Time, lane publishing.PublicationLane) {
	t.Helper()
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: id, CanonicalURL: canonicalURL, Status: article.StatusPendingPublication, FetchedAt: now, PublishedAt: now,
		Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := outbox.EnqueueLane(context.Background(), id, "{}", lane, now); err != nil {
		t.Fatalf("enqueue lane %q: %v", lane, err)
	}
}

func TestLiveLane_BypassesNormalLaneBurstThrottle(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	now := time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC)

	const normalCount, liveCount = 5, 5
	for i := 0; i < normalCount; i++ {
		seedRealLaneArticle(t, db, fmt.Sprintf("normal-%d", i), fmt.Sprintf("https://example.com/normal-%d", i), now, publishing.LaneNormal)
	}
	for i := 0; i < liveCount; i++ {
		seedRealLaneArticle(t, db, fmt.Sprintf("live-%d", i), fmt.Sprintf("https://example.com/live-%d", i), now, publishing.LaneLive)
	}

	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	publisher := telegramadapter.New(telegramadapter.Config{ChannelID: "@test", BotToken: "test-token", BaseURL: srv.URL, Timeout: 100 * time.Millisecond})

	// The exact production shape this whole plan is fixing: burst caps at
	// 1 per 15 minutes for the normal lane, while the live lane (0 =
	// disabled check, per policy.go:72) stays unthrottled.
	restrictivePolicy := publishing.PolicyConfig{
		BurstMaxPerWindow: 1, BurstWindow: 15 * time.Minute,
		Live: &publishing.PolicyConfig{BurstMaxPerWindow: 0},
	}
	uc := usecase.NewPublishOutbox(
		articles, outbox, decisions, uow, publisher, testBackoff, restrictivePolicy,
		"worker-1", 20, time.Minute, fixedClock{now}, fakeLogger{}, clusterPublications, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10}, nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Published != liveCount+1 {
		t.Fatalf("Published = %d, want %d (all %d live-lane items, plus exactly 1 normal-lane item admitted by the burst cap)", result.Published, liveCount+1, liveCount)
	}
	if result.Deferred != normalCount-1 {
		t.Fatalf("Deferred = %d, want %d (the remaining normal-lane items past the burst cap)", result.Deferred, normalCount-1)
	}
	if got := srv.Calls(); got != liveCount+1 {
		t.Fatalf("srv.Calls() = %d, want %d", got, liveCount+1)
	}
}
