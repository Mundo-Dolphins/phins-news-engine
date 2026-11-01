package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type stubPublisher struct{}

func (stubPublisher) Publish(context.Context, article.Article) (string, error) { return "id", nil }

func newBarePublishOutbox() *usecase.PublishOutbox {
	return usecase.NewPublishOutbox(nil, nil, nil, nil, stubPublisher{}, publishing.BackoffPolicy{}, publishing.PolicyConfig{}, "w", 1, time.Minute, nil, nil, nil, nil, usecase.ReconciliationConfig{}, nil)
}

func TestRegisterPublishersAcceptsBlueskyAndRejectsTelegramOverride(t *testing.T) {
	ctx := context.Background()
	if err := registerPublishers(ctx, newBarePublishOutbox(), map[publishing.Destination]out.Publisher{publishing.DestinationBluesky: stubPublisher{}}); err != nil {
		t.Fatalf("bluesky: %v", err)
	}
	if err := registerPublishers(ctx, newBarePublishOutbox(), map[publishing.Destination]out.Publisher{publishing.DestinationTelegram: stubPublisher{}}); err == nil {
		t.Fatal("overriding telegram accepted")
	}
	if err := registerPublishers(ctx, newBarePublishOutbox(), nil); err != nil {
		t.Fatalf("no extra publishers: %v", err)
	}
}
