package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// These tests isolate PublishOutbox's own Confirm/Release wiring onto
// out.SourcePolicyCooldownRepository (Task 9) using the stateful
// fakeSourcePolicyCooldownRepository from fakes_test.go (added in Task 8),
// rather than the real SQLite adapter Task 11's integration tests already
// exercise end-to-end. Each test pre-seeds the fake as already holding a
// "reserved" row for the article under test — as Gate B's Reserve call
// would have left it before PublishOutbox ever ran — then drives one
// PublishOutbox.Run() outcome and asserts on the fake's resulting state
// plus the result counters finalizeDelivered/recordFailure increment.

// seedReservedCooldown pre-seeds cooldown as already holding a "reserved"
// row for (sourceID, clusterID, articleID), mirroring what Gate B's Reserve
// would have left before PublishOutbox ever ran.
func seedReservedCooldown(cooldown *fakeSourcePolicyCooldownRepository, sourceID, clusterID, articleID string) {
	cooldown.state = map[string]string{}
	cooldown.rows = map[string]out.SourcePolicyCooldownReservation{}
	cooldown.cooldownUntil = map[string]time.Time{}
	key := sourceID + "|" + clusterID
	cooldown.state[key] = "reserved"
	cooldown.rows[key] = out.SourcePolicyCooldownReservation{SourceID: sourceID, ClusterID: clusterID, ArticleID: articleID}
	cooldown.cooldownUntil[key] = time.Now().Add(time.Hour)
}

func TestPublishOutbox_Cooldown_ConfirmsOnDelivery(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	sourceID := mustID(t, source.TypeRSS, "s1")
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, Source: source.Source{ID: sourceID, Type: source.TypeRSS}, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	cooldown := &fakeSourcePolicyCooldownRepository{}
	seedReservedCooldown(cooldown, sourceID.String(), "c1", "a1")
	pub := &fakePublisher{}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, cooldown,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	key := sourceID.String() + "|c1"
	if cooldown.state[key] != "confirmed" {
		t.Errorf("cooldown.state[%q] = %q, want confirmed", key, cooldown.state[key])
	}
	if result.CooldownConfirmed != 1 {
		t.Errorf("result.CooldownConfirmed = %d, want 1", result.CooldownConfirmed)
	}
}

func TestPublishOutbox_Cooldown_ReleasesOnPermanentFailure(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	sourceID := mustID(t, source.TypeRSS, "s1")
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, Source: source.Source{ID: sourceID, Type: source.TypeRSS}, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	cooldown := &fakeSourcePolicyCooldownRepository{}
	seedReservedCooldown(cooldown, sourceID.String(), "c1", "a1")
	pub := &fakePublisher{err: errors.New("article rejected as malformed"), permanent: true}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, cooldown,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Discarded != 1 {
		t.Fatalf("result = %+v, want discarded=1", result)
	}
	key := sourceID.String() + "|c1"
	if cooldown.state[key] != "released" {
		t.Errorf("cooldown.state[%q] = %q, want released", key, cooldown.state[key])
	}
	if result.CooldownReleased != 1 {
		t.Errorf("result.CooldownReleased = %d, want 1", result.CooldownReleased)
	}
}

func TestPublishOutbox_Cooldown_DoesNotReleaseOnRetryableFailure(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	sourceID := mustID(t, source.TypeRSS, "s1")
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, Source: source.Source{ID: sourceID, Type: source.TypeRSS}, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	// AttemptCount 0 -> nextAttempt=1, well under testBackoff's MaxAttempts=6,
	// so backoff.Exhausted(1) is false and this is a purely retryable failure.
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 0}}}
	cooldown := &fakeSourcePolicyCooldownRepository{}
	seedReservedCooldown(cooldown, sourceID.String(), "c1", "a1")
	pub := &fakePublisher{err: errors.New("telegram unreachable")}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, cooldown,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Retried != 1 {
		t.Fatalf("result = %+v, want retried=1", result)
	}
	key := sourceID.String() + "|c1"
	if cooldown.state[key] != "reserved" {
		t.Errorf("cooldown.state[%q] = %q, want still reserved: a merely-retrying failure must not release the cooldown", key, cooldown.state[key])
	}
	if result.CooldownReleased != 0 {
		t.Errorf("result.CooldownReleased = %d, want 0", result.CooldownReleased)
	}
}
