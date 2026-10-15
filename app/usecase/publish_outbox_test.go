package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var testBackoff = publishing.BackoffPolicy{BaseDelay: 30 * time.Second, MaxDelay: 30 * time.Minute, MaxAttempts: 6}

func newPublishOutbox(articles *fakeArticleRepo, outbox *fakeOutboxRepo, decisions *fakeDecisionRepo, uow *fakeUnitOfWork, pub *fakePublisher, clock fakeClock, clusterPublications *fakeClusterPublicationRepository) *usecase.PublishOutbox {
	return usecase.NewPublishOutbox(articles, outbox, decisions, uow, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, clock, fakeLogger{}, clusterPublications, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
}

// allowAllPolicy is the zero-value-friendly policy every existing test
// implicitly relies on: burst/same-topic disabled (<=0 max), quiet hours
// off — i.e. publication_policy.Evaluate always returns Allow=true. Tests
// that specifically exercise deferral build their own publishing.PolicyConfig.
var allowAllPolicy = publishing.PolicyConfig{}

func seedPendingArticle(t *testing.T, repo *fakeArticleRepo, id string) {
	t.Helper()
	if err := repo.Save(context.Background(), article.Article{ID: id, Status: article.StatusPendingPublication}); err != nil {
		t.Fatalf("seed article %s: %v", id, err)
	}
}

func TestPublishOutbox_PublishesSuccessfully(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	decisions := &fakeDecisionRepo{}
	pub := &fakePublisher{}
	uc := newPublishOutbox(repo, outbox, decisions, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Claimed != 1 || result.Published != 1 || result.Retried != 0 || result.Discarded != 0 {
		t.Errorf("result = %+v, want claimed=1 published=1", result)
	}
	if len(pub.published) != 1 || pub.published[0].ID != "a1" {
		t.Errorf("pub.published = %v, want [a1]", pub.published)
	}
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 1 {
		t.Errorf("outbox.delivered = %v, want [1]", outbox.delivered)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusPublished {
		t.Errorf("final status = %v, want %v", final.Status, article.StatusPublished)
	}
	if len(decisions.events) != 1 || decisions.events[0].ToStatus != article.StatusPublished {
		t.Errorf("decisions.events = %+v, want 1 event ending in Published", decisions.events)
	}
}

func TestPublishOutbox_RevalidatesStoredFreshnessAtBoundaryForEveryContentType(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		contentType article.ContentType
		maxAge      time.Duration
	}{
		{"hard-news", article.ContentTypeArticle, 1 * time.Hour},
		{"social", article.ContentTypeSocialPost, 24 * time.Hour},
		{"analysis", article.ContentTypeVideo, 7 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, offset := range []struct {
				name    string
				age     time.Duration
				expired bool
			}{
				{"just-before", tc.maxAge - time.Nanosecond, false},
				{"exact-limit", tc.maxAge, false},
				{"just-after", tc.maxAge + time.Nanosecond, true},
			} {
				t.Run(offset.name, func(t *testing.T) {
					repo := &fakeArticleRepo{}
					if err := repo.Save(context.Background(), article.Article{
						ID: "a1", Status: article.StatusPendingPublication, ContentType: tc.contentType,
						PublishedAt: now.Add(-offset.age), FreshnessMaxAge: tc.maxAge,
						// available_at is intentionally unrelated and in the future.
					}); err != nil {
						t.Fatal(err)
					}
					outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AvailableAt: now.Add(30 * 24 * time.Hour)}}}
					pub := &fakePublisher{}
					uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

					result, err := uc.Run(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if offset.expired {
						if result.Expired != 1 || len(pub.published) != 0 || len(outbox.attempts) != 0 || len(outbox.expired) != 1 {
							t.Fatalf("result=%+v published=%d attempts=%d expired=%v", result, len(pub.published), len(outbox.attempts), outbox.expired)
						}
						if got := repo.lastSaved(); got.Status != article.StatusDiscarded || got.StatusReason != "publish_expired" {
							t.Fatalf("article=%+v, want discarded/publish_expired", got)
						}
					} else if result.Published != 1 || len(pub.published) != 1 {
						t.Fatalf("result=%+v published=%d, want one publish", result, len(pub.published))
					}
				})
			}
		})
	}
}

func TestPublishOutbox_ExpiredReleasesReservationsWithoutAttempt(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	sourceID := mustID(t, source.TypeRSS, "test-source")
	articles := &fakeArticleRepo{}
	if err := articles.Save(context.Background(), article.Article{
		ID: "a1", Status: article.StatusPendingPublication, ClusterID: "cluster-1", Source: source.Source{ID: sourceID, Type: source.TypeRSS},
		PublishedAt: now.Add(-2 * time.Hour), FreshnessMaxAge: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 4}}}
	gate := &fakeClusterPublicationRepository{}
	cooldown := &fakeSourcePolicyCooldownRepository{}
	if _, err := gate.Reserve(context.Background(), out.ClusterPublicationReservation{ClusterID: "cluster-1", ArticleID: "a1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := cooldown.Reserve(context.Background(), out.SourcePolicyCooldownReservation{SourceID: sourceID.String(), ClusterID: "cluster-1", ArticleID: "a1"}, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	pub := &fakePublisher{}
	uc := usecase.NewPublishOutbox(articles, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, gate, cooldown, usecase.ReconciliationConfig{BatchSize: 10}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != 1 || result.CooldownReleased != 1 || len(pub.published) != 0 || len(outbox.attempts) != 0 || len(outbox.expired) != 1 {
		t.Fatalf("result=%+v published=%d attempts=%d expired=%v releases=%v/%v", result, len(pub.published), len(outbox.attempts), outbox.expired, gate.released, cooldown.released)
	}
	if len(gate.released) != 1 || len(cooldown.released) != 1 {
		t.Fatalf("reservations not released: cluster=%v cooldown=%v", gate.released, cooldown.released)
	}
}

func TestPublishOutbox_RunSweepsExpiredBacklogBeforeClaiming(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	articles := &fakeArticleRepo{}
	if err := articles.Save(context.Background(), article.Article{
		ID: "stale", Status: article.StatusPendingPublication, ClusterID: "cluster-1",
		PublishedAt: now.Add(-3 * time.Hour), FreshnessMaxAge: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	outbox := &fakeOutboxRepo{claimExpiredItems: []out.OutboxItem{{ID: 7, ArticleID: "stale", ExpiresAt: now.Add(-2 * time.Hour)}}}
	decisions := &fakeDecisionRepo{}
	gate := &fakeClusterPublicationRepository{}
	pub := &fakePublisher{}
	uc := newPublishOutbox(articles, outbox, decisions, &fakeUnitOfWork{}, pub, fakeClock{now: now}, gate)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != 1 || result.Claimed != 0 || len(pub.published) != 0 || len(outbox.expired) != 1 || outbox.expired[0] != 7 {
		t.Fatalf("result=%+v published=%d expired=%v, want one swept expiry and no publish", result, len(pub.published), outbox.expired)
	}
	if got := articles.lastSaved(); got.Status != article.StatusDiscarded || got.StatusReason != usecase.PublishExpiredReasonCode {
		t.Fatalf("article=%+v, want discarded/%s", got, usecase.PublishExpiredReasonCode)
	}
	if len(gate.released) != 1 {
		t.Fatalf("cluster gate released = %v, want 1", gate.released)
	}
	if len(decisions.events) != 1 || decisions.events[0].EventType != usecase.PublishExpiredEventType {
		t.Fatalf("events = %+v, want one %s", decisions.events, usecase.PublishExpiredEventType)
	}
	sweep, claim := -1, -1
	for i, c := range outbox.calls {
		switch c {
		case "claim_expired":
			sweep = i
		case "claim":
			claim = i
		}
	}
	if sweep < 0 || claim < 0 || sweep > claim {
		t.Fatalf("calls = %v, want claim_expired before claim", outbox.calls)
	}
}

func TestPublishOutbox_RunPropagatesExpiredSweepError(t *testing.T) {
	outbox := &fakeOutboxRepo{claimExpiredErr: errors.New("boom")}
	uc := newPublishOutbox(&fakeArticleRepo{}, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: time.Now()}, &fakeClusterPublicationRepository{})
	if _, err := uc.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want sweep error")
	}
}

func TestPublishOutbox_ExpiredRollbackDoesNotCountOrSave(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, PublishedAt: now.Add(-2 * time.Hour), FreshnessMaxAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{err: errors.New("rollback")}, &fakePublisher{}, fakeClock{now: now}, &fakeClusterPublicationRepository{})
	result, err := uc.Run(context.Background())
	if err == nil || result.Expired != 0 || len(outbox.expired) != 0 || repo.lastSaved().Status != article.StatusPendingPublication {
		t.Fatalf("result=%+v err=%v expired=%v article=%+v", result, err, outbox.expired, repo.lastSaved())
	}
}

func TestPublishOutbox_TemporaryFailureSchedulesRetry(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 0}}}
	pub := &fakePublisher{err: errors.New("telegram unreachable")}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Retried != 1 || result.Discarded != 0 {
		t.Errorf("result = %+v, want retried=1", result)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusPendingRetry {
		t.Errorf("status = %v, want %v", final.Status, article.StatusPendingRetry)
	}
	if len(outbox.retryable) != 1 {
		t.Errorf("outbox.retryable = %v, want 1 entry", outbox.retryable)
	}
}

func TestPublishOutbox_PermanentFailureDiscardsImmediately(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 0}}}
	pub := &fakePublisher{err: errors.New("article rejected as malformed"), permanent: true}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Discarded != 1 || result.Retried != 0 {
		t.Errorf("result = %+v, want discarded=1 (permanent errors skip retry regardless of attempt count)", result)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusDiscarded {
		t.Errorf("status = %v, want %v", final.Status, article.StatusDiscarded)
	}
}

func TestPublishOutbox_ExhaustedRetriesDiscards(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	// AttemptCount 5 -> this is the 6th attempt -> Exhausted(6) with MaxAttempts=6.
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 5}}}
	pub := &fakePublisher{err: errors.New("telegram unreachable")}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Discarded != 1 {
		t.Errorf("result = %+v, want discarded=1 (retry budget exhausted)", result)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusDiscarded {
		t.Errorf("status = %v, want %v", final.Status, article.StatusDiscarded)
	}
}

func TestPublishOutbox_NoItemsClaimedIsNoop(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	outbox := &fakeOutboxRepo{}
	pub := &fakePublisher{}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Claimed != 0 || result.Published != 0 || result.Retried != 0 || result.Discarded != 0 {
		t.Errorf("result = %+v, want all zero", result)
	}
	if len(pub.published) != 0 {
		t.Error("publisher should not have been called")
	}
}

func TestPublishOutbox_FindByIDFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{} // "a1" was never Saved, so FindByID fails
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	_, err := uc.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want a wrapped find error")
	}
}

func TestPublishOutbox_BurstLimitDefersWithoutIncrementingAttempts(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}, succeededSince: 6}
	pub := &fakePublisher{}
	policy := publishing.PolicyConfig{BurstMaxPerWindow: 6, BurstWindow: time.Hour}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, policy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Deferred != 1 || result.Published != 0 {
		t.Errorf("result = %+v, want deferred=1 published=0", result)
	}
	if len(outbox.deferred) != 1 || outbox.deferred[0] != 1 {
		t.Errorf("outbox.deferred = %v, want [1]", outbox.deferred)
	}
	if len(pub.published) != 0 {
		t.Error("publisher should not have been called when the burst limit is hit")
	}
}

func TestPublishOutbox_SameTopicLimitDefersUsingArticlePrimaryEntity(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, PrimaryEntity: "Jane Doe"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}, succeededByEntity: map[string]int{"Jane Doe": 2}}
	policy := publishing.PolicyConfig{SameTopicMaxPerWindow: 2, SameTopicWindow: 3 * time.Hour}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, policy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Deferred != 1 {
		t.Errorf("result = %+v, want deferred=1", result)
	}
}

func TestPublishOutbox_SuccessRecordsExternalMessageID(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	pub := &fakePublisher{messageID: "12345"}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(outbox.attempts) != 1 || outbox.attempts[0].ExternalMessageID != "12345" {
		t.Errorf("outbox.attempts = %+v, want ExternalMessageID=12345", outbox.attempts)
	}
}

func TestPublishOutbox_RateLimitedErrorExtendsBackoffPastComputedDelay(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 0}}}
	// testBackoff's first-attempt delay is 30s; RetryAfter (5 minutes) must win.
	pub := &fakePublisher{err: &out.RateLimitedError{Err: errors.New("429"), RetryAfter: 5 * time.Minute}}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(outbox.retryable) != 1 {
		t.Fatalf("outbox.retryable = %v, want 1 entry", outbox.retryable)
	}
	if len(outbox.retryableAvailableAt) != 1 || !outbox.retryableAvailableAt[0].Equal(now.Add(5*time.Minute)) {
		t.Errorf("retryableAvailableAt = %v, want [%v] (RetryAfter wins over the 30s base backoff)", outbox.retryableAvailableAt, now.Add(5*time.Minute))
	}
}

func TestPublishOutbox_RateLimitedErrorDefersRemainingClaimedItems(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	seedPendingArticle(t, repo, "a2")
	seedPendingArticle(t, repo, "a3")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{
		{ID: 1, ArticleID: "a1", AttemptCount: 0},
		{ID: 2, ArticleID: "a2", AttemptCount: 0},
		{ID: 3, ArticleID: "a3", AttemptCount: 0},
	}}
	pub := &fakePublisher{err: &out.RateLimitedError{Err: errors.New("429"), RetryAfter: 5 * time.Minute}}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Retried != 1 || result.Deferred != 2 || result.Published != 0 {
		t.Fatalf("result = %+v, want retried=1 deferred=2 published=0", result)
	}
	if len(outbox.retryable) != 1 || outbox.retryable[0] != 1 {
		t.Fatalf("retryable = %v, want [1]", outbox.retryable)
	}
	if len(outbox.deferred) != 2 || outbox.deferred[0] != 2 || outbox.deferred[1] != 3 {
		t.Fatalf("deferred = %v, want [2 3]", outbox.deferred)
	}
	for _, got := range outbox.deferredAvailableAt {
		if !got.Equal(now.Add(5 * time.Minute)) {
			t.Fatalf("deferredAvailableAt = %v, want all %v", outbox.deferredAvailableAt, now.Add(5*time.Minute))
		}
	}
	if len(pub.published) != 0 {
		t.Fatalf("published calls = %v, want no successful publishes", pub.published)
	}
}

func TestPublishOutbox_SuccessConfirmsClusterGate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	gate := &fakeClusterPublicationRepository{}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, gate, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(gate.confirmed) != 1 || gate.confirmed[0] != "c1" {
		t.Errorf("gate.confirmed = %v, want [c1]", gate.confirmed)
	}
	if len(gate.released) != 0 {
		t.Errorf("gate.released = %v, want none on success", gate.released)
	}
}

func TestPublishOutbox_TemporaryFailureDoesNotReleaseClusterGate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 0}}}
	pub := &fakePublisher{err: errors.New("telegram unreachable")}
	gate := &fakeClusterPublicationRepository{}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, gate, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Retried != 1 {
		t.Fatalf("result = %+v, want retried=1", result)
	}
	if len(gate.released) != 0 {
		t.Errorf("gate.released = %v, want none: a merely-retrying failure must not open a duplicate path", gate.released)
	}
	if len(gate.confirmed) != 0 {
		t.Errorf("gate.confirmed = %v, want none on a failed attempt", gate.confirmed)
	}
}

func TestPublishOutbox_PermanentFailureReleasesClusterGate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	pub := &fakePublisher{err: errors.New("article rejected as malformed"), permanent: true}
	gate := &fakeClusterPublicationRepository{}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, gate, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Discarded != 1 {
		t.Fatalf("result = %+v, want discarded=1", result)
	}
	if len(gate.released) != 1 || gate.released[0] != "c1" {
		t.Errorf("gate.released = %v, want [c1]: a permanently failed delivery must release the cluster for a future article", gate.released)
	}
}

func TestPublishOutbox_ExhaustedRetriesReleasesClusterGate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1", AttemptCount: 5}}}
	pub := &fakePublisher{err: errors.New("telegram unreachable")}
	gate := &fakeClusterPublicationRepository{}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, gate, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Discarded != 1 {
		t.Fatalf("result = %+v, want discarded=1", result)
	}
	if len(gate.released) != 1 || gate.released[0] != "c1" {
		t.Errorf("gate.released = %v, want [c1]: retry-budget exhaustion must release the cluster too", gate.released)
	}
}

func TestPublishOutbox_AmbiguousOutcomeHoldsForReconciliation(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	pub := &fakePublisher{err: &out.AmbiguousError{Err: errors.New("timeout")}}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.AmbiguousDetected != 1 || result.Published != 0 || result.Retried != 0 || result.Discarded != 0 {
		t.Errorf("result = %+v, want AmbiguousDetected=1 and no other bucket touched", result)
	}
	if outbox.sentUnconfirmed[1].AmbiguousReason == "" {
		t.Error("RecordSentUnconfirmed was not called with a reason")
	}
	if len(outbox.retryable) != 0 || len(outbox.discarded) != 0 || len(outbox.delivered) != 0 {
		t.Error("an ambiguous outcome must not touch retryable/discarded/delivered directly — only Reconcile may")
	}
}

func TestPublishOutbox_SuccessRecordsEvidenceBeforeFinalizing(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	pub := &fakePublisher{messageID: "999"}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outbox.sentUnconfirmed[1].SentMessageID != "999" {
		t.Errorf("sentUnconfirmed[1] = %+v, want SentMessageID=999 recorded even on the synchronous-success path", outbox.sentUnconfirmed[1])
	}
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 1 {
		t.Errorf("outbox.delivered = %v, want [1]", outbox.delivered)
	}
}

// TestPublishOutbox_SuccessEvidencePersistsEvenIfFinalizeTransactionFails is
// the regression test for the exact bug this task exists to fix: if
// RecordSentUnconfirmed were ever moved inside finalizeDelivered's uow.Do
// closure, a failing uow would roll back the evidence write along with the
// finalize — and every other success-path test would still pass, since none
// of them exercise a failing uow. Here the uow always fails, so this test
// only passes if RecordSentUnconfirmed truly commits outside/before
// finalizeDelivered's transaction.
func TestPublishOutbox_SuccessEvidencePersistsEvenIfFinalizeTransactionFails(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}}
	pub := &fakePublisher{messageID: "999"}
	failingUow := &fakeUnitOfWork{err: errors.New("uow down")}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, failingUow, pub, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	if _, err := uc.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want the uow failure to propagate")
	}
	if outbox.sentUnconfirmed[1].SentMessageID != "999" {
		t.Errorf("sentUnconfirmed[1] = %+v, want SentMessageID=999 recorded even though the finalize transaction failed", outbox.sentUnconfirmed[1])
	}
	if len(outbox.delivered) != 0 {
		t.Errorf("outbox.delivered = %v, want none: the finalize transaction must never have committed", outbox.delivered)
	}
}

func TestPublishOutbox_ReconcilePositiveViaKnownMessageID(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{ID: 1, ArticleID: "a1", SentMessageID: "42"}}}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.ReconciledPositive != 1 {
		t.Errorf("result = %+v, want ReconciledPositive=1", result)
	}
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 1 {
		t.Errorf("outbox.delivered = %v, want [1] — a known message_id needs no network call to reconcile", outbox.delivered)
	}
}

func TestPublishOutbox_ReconcileQuarantinesWhenNoImporterConfigured(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{ID: 1, ArticleID: "a1"}}} // no SentMessageID: bucket C
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Quarantined != 1 || result.ReconciledPositive != 0 || result.ReconciledNegative != 0 {
		t.Errorf("result = %+v, want Quarantined=1", result)
	}
	if len(outbox.quarantined) != 1 || outbox.quarantined[0] != 1 {
		t.Errorf("outbox.quarantined = %v, want [1]", outbox.quarantined)
	}
}

func TestPublishOutbox_ReconcilePositiveViaChannelHistory(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPublishing, CanonicalURL: "https://example.com/a"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{ID: 1, ArticleID: "a1", SentAt: now.Add(-time.Minute)}}}
	importer := &fakeChannelImporter{messages: []telegramimport.Message{
		{ID: 7, PostedAt: now.Add(-30 * time.Second), Text: "Leer más: https://example.com/a"},
	}}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, importer)

	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.ReconciledPositive != 1 {
		t.Errorf("result = %+v, want ReconciledPositive=1", result)
	}
	if len(outbox.delivered) != 1 {
		t.Errorf("outbox.delivered = %v, want 1 entry", outbox.delivered)
	}
}

func TestPublishOutbox_ReconcileNegativeViaChannelHistoryMakesItRetryable(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPublishing, CanonicalURL: "https://example.com/a"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{ID: 1, ArticleID: "a1", SentAt: now.Add(-time.Minute)}}}
	importer := &fakeChannelImporter{messages: []telegramimport.Message{
		{ID: 7, PostedAt: now.Add(-2 * time.Hour), Text: "https://example.com/unrelated"}, // covers well past SentAt, no match
	}}
	decisions := &fakeDecisionRepo{}
	uc := usecase.NewPublishOutbox(repo, outbox, decisions, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, importer)

	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.ReconciledNegative != 1 {
		t.Errorf("result = %+v, want ReconciledNegative=1", result)
	}
	if len(outbox.retryable) != 1 {
		t.Errorf("outbox.retryable = %v, want 1 entry — confirmed non-delivery is safe to retry", outbox.retryable)
	}
	// A reconciled-negative outcome must be exactly as auditable as any
	// other failed publish attempt: article status moved off Publishing,
	// a publication_attempts row recorded, and a decision event appended.
	final, err := repo.FindByID(context.Background(), "a1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if final.Status != article.StatusPendingRetry {
		t.Errorf("final.Status = %v, want %v", final.Status, article.StatusPendingRetry)
	}
	if final.StatusReason == "" {
		t.Error("final.StatusReason = \"\", want a reconciliation-outcome reason")
	}
	if len(outbox.attempts) != 1 || outbox.attempts[0].Outcome != publishing.AttemptTemporaryFailure {
		t.Errorf("outbox.attempts = %+v, want 1 AttemptTemporaryFailure entry", outbox.attempts)
	}
	if len(decisions.events) != 1 || decisions.events[0].EventType != "reconcile_negative" {
		t.Errorf("decisions.events = %+v, want 1 event with EventType=reconcile_negative", decisions.events)
	}
}

func TestPublishOutbox_ReconcileQuarantinesOnInconclusiveChannelHistory(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPublishing, CanonicalURL: "https://example.com/a"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{ID: 1, ArticleID: "a1", SentAt: now.Add(-time.Hour)}}}
	importer := &fakeChannelImporter{messages: []telegramimport.Message{
		{ID: 7, PostedAt: now.Add(-time.Minute), Text: "https://example.com/unrelated"}, // does not reach back to SentAt
	}}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, importer)

	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Quarantined != 1 {
		t.Errorf("result = %+v, want Quarantined=1 (insufficient history coverage must never count as a negative)", result)
	}
}

func TestPublishOutbox_ReconcileQuarantinesOnImporterError(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPublishing, CanonicalURL: "https://example.com/a"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{ID: 1, ArticleID: "a1", SentAt: now.Add(-time.Hour)}}}
	importer := &fakeChannelImporter{err: errors.New("flood wait")}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, importer)

	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Quarantined != 1 {
		t.Errorf("result = %+v, want Quarantined=1", result)
	}
}

// TestPublishOutbox_ReconcileFetchesChannelHistoryOnceForMultipleItems is the
// regression test for the flood-wait risk of calling ExportChannelMessages
// once per sent_unconfirmed item: with two items in the same pass, neither
// carrying a local message_id, the channel-history fetch must still happen
// only once and be reused for both — never N fetches against the same
// channel per tick.
func TestPublishOutbox_ReconcileFetchesChannelHistoryOnceForMultipleItems(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	if err := repo.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPublishing, CanonicalURL: "https://example.com/a"}); err != nil {
		t.Fatalf("seed article a1: %v", err)
	}
	if err := repo.Save(context.Background(), article.Article{ID: "a2", Status: article.StatusPublishing, CanonicalURL: "https://example.com/b"}); err != nil {
		t.Fatalf("seed article a2: %v", err)
	}
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{
		{ID: 1, ArticleID: "a1", SentAt: now.Add(-time.Minute)},
		{ID: 2, ArticleID: "a2", SentAt: now.Add(-time.Minute)},
	}}
	importer := &fakeChannelImporter{messages: []telegramimport.Message{
		{ID: 7, PostedAt: now.Add(-30 * time.Second), Text: "Leer más: https://example.com/a"},
	}}
	uc := usecase.NewPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, importer)

	if _, err := uc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if importer.calls != 1 {
		t.Errorf("importer.calls = %d, want 1 — the channel-history fetch must be shared across every item in the pass", importer.calls)
	}
}

// TestPublishOutbox_RunCallsReconcileBeforeClaiming seeds both a
// sent_unconfirmed item (bucket A, resolved by Reconcile) and a normal
// pending outbox item (resolved by ClaimBatch) in the same Run, and asserts
// on fakeOutboxRepo.calls's recorded order — not just on the folded-in
// result counters, which would hold identically even if ClaimBatch ran
// first, since ClaimBatch's own outcome doesn't depend on Reconcile having
// run. The call-order assertion is the only thing that actually proves
// Reconcile executes before ClaimBatch.
func TestPublishOutbox_RunCallsReconcileBeforeClaiming(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repo := &fakeArticleRepo{}
	seedPendingArticle(t, repo, "a1")
	seedPendingArticle(t, repo, "a2")
	outbox := &fakeOutboxRepo{
		claimItems:          []out.OutboxItem{{ID: 2, ArticleID: "a2"}},
		sentUnconfirmedList: []out.OutboxItem{{ID: 1, ArticleID: "a1", SentMessageID: "42"}},
	}
	uc := newPublishOutbox(repo, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: now}, &fakeClusterPublicationRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ReconciledPositive != 1 {
		t.Errorf("result = %+v, want Run() to have folded in Reconcile's outcome", result)
	}
	if result.Published != 1 {
		t.Errorf("result = %+v, want the claimed item published too", result)
	}
	if want := []string{"reconcile", "claim_expired", "claim"}; len(outbox.calls) != len(want) || outbox.calls[0] != want[0] || outbox.calls[1] != want[1] || outbox.calls[2] != want[2] {
		t.Errorf("outbox.calls = %v, want %v — Reconcile must run before ClaimBatch", outbox.calls, want)
	}
}
