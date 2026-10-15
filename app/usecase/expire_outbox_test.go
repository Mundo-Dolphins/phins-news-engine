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
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeExpirationRepository struct {
	candidates []out.OutboxExpirationCandidate
	expired    []int64
	err        error
}

func (r *fakeExpirationRepository) ListExpirationCandidates(context.Context, out.OutboxExpirationFilter) ([]out.OutboxExpirationCandidate, error) {
	return r.candidates, r.err
}

func (r *fakeExpirationRepository) ExpireClaimable(_ context.Context, id int64, _ string, _ string, _ time.Time) error {
	if r.err != nil {
		return r.err
	}
	r.expired = append(r.expired, id)
	return nil
}

func TestExpireOutbox_PreviewIsReadOnly(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	repo := &fakeExpirationRepository{candidates: []out.OutboxExpirationCandidate{{OutboxID: 1, ArticleID: "a1", Status: publishing.OutboxPending}}}
	uc := usecase.NewExpireOutbox(repo, &fakeArticleRepo{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	preview, err := uc.Preview(context.Background(), usecase.ExpireOutboxRequest{OlderThan: 24 * time.Hour, Limit: 100})
	if err != nil || len(preview.Candidates) != 1 {
		t.Fatalf("Preview() = %+v, %v, want one candidate", preview, err)
	}
	if len(repo.expired) != 0 {
		t.Fatalf("expired = %v, want none during preview", repo.expired)
	}
}

func TestExpireOutbox_ApplyDiscardsAuditsAndReleasesReservations(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	sourceID := mustID(t, source.TypeRSS, "test-source")
	articles := &fakeArticleRepo{byID: map[string]article.Article{"a1": {ID: "a1", Status: article.StatusPendingRetry, ClusterID: "c1", Source: source.Source{ID: sourceID}}}}
	gate := repairGate("c1")
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	if _, err := cooldowns.Reserve(context.Background(), out.SourcePolicyCooldownReservation{SourceID: sourceID.String(), ClusterID: "c1", ArticleID: "a1"}, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	decisions := &fakeDecisionRepo{}
	repo := &fakeExpirationRepository{}
	uc := usecase.NewExpireOutbox(repo, articles, gate, cooldowns, decisions, &fakeUnitOfWork{}, fakeClock{now: now})

	applied, err := uc.Apply(context.Background(), []out.OutboxExpirationCandidate{{OutboxID: 7, ArticleID: "a1"}}, "game preview is obsolete")
	if err != nil || applied != 1 {
		t.Fatalf("Apply() = %d, %v, want 1, nil", applied, err)
	}
	if got := articles.lastSaved(); got.Status != article.StatusDiscarded || got.StatusReason != usecase.OutboxExpiredReasonCode+": game preview is obsolete" {
		t.Errorf("saved article = %+v, want discarded with expiration reason", got)
	}
	if gate.state["c1"] != "released" || cooldowns.state[cooldownKey(sourceID.String(), "c1")] != "released" {
		t.Errorf("reservation states = cluster:%q cooldown:%q, want released/released", gate.state["c1"], cooldowns.state[cooldownKey(sourceID.String(), "c1")])
	}
	if len(decisions.events) != 1 || decisions.events[0].ReasonCode != usecase.OutboxExpiredReasonCode {
		t.Errorf("events = %+v, want one expiration audit", decisions.events)
	}
}

func TestExpireOutbox_ApplyPropagatesPartialFailure(t *testing.T) {
	repo := &fakeExpirationRepository{err: errors.New("write failed")}
	articles := &fakeArticleRepo{byID: map[string]article.Article{"a1": {ID: "a1", Status: article.StatusPendingPublication}}}
	uc := usecase.NewExpireOutbox(repo, articles, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{})
	if _, err := uc.Apply(context.Background(), []out.OutboxExpirationCandidate{{OutboxID: 1, ArticleID: "a1"}}, "obsolete"); err == nil {
		t.Fatal("Apply() error = nil, want repository failure")
	}
}
