package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// reconcilingPublisher is a destination publisher that can also look up its
// own history (out.DestinationReconciler).
type reconcilingPublisher struct {
	fakePublisher
	id      string
	matched bool
	covered bool
	err     error
	lookups []out.SentLookup
}

func (r *reconcilingPublisher) FindSent(_ context.Context, l out.SentLookup) (publishing.PostRef, bool, bool, error) {
	r.lookups = append(r.lookups, l)
	return publishing.PostRef{URI: r.id}, r.matched, r.covered, r.err
}

func reconcileBlueskySetup(t *testing.T, pub *reconcilingPublisher) (*fakeOutboxRepo, *fakeArticleRepo, func() error) {
	t.Helper()
	articles := &fakeArticleRepo{}
	if err := articles.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPublishing, CanonicalURL: "https://example.com/a"}); err != nil {
		t.Fatal(err)
	}
	payload, _ := publishing.MessagePayload{Text: "Informe"}.Marshal()
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{
		ID: 1, ArticleID: "a1", Destination: publishing.DestinationBluesky, Payload: payload, SentAt: destNow.Add(-time.Minute),
	}}}
	uc := newPublishOutboxWithImporter(articles, outbox, nil)
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, pub); err != nil {
		t.Fatal(err)
	}
	return outbox, articles, func() error {
		res, err := uc.Reconcile(context.Background())
		if err != nil {
			return err
		}
		if res.Checked != 1 {
			return errors.New("row not checked")
		}
		return nil
	}
}

func TestPublishOutbox_ReconcileUsesDestinationPublisherHistory(t *testing.T) {
	tests := []struct {
		name          string
		pub           *reconcilingPublisher
		wantDelivered bool
		wantRetryable bool
		wantQuarantin bool
	}{
		{"match finalizes", &reconcilingPublisher{id: "at://d/p/1", matched: true, covered: true}, true, false, false},
		{"covered non-match is retryable", &reconcilingPublisher{covered: true}, false, true, false},
		{"inconclusive quarantines", &reconcilingPublisher{}, false, false, true},
		{"lookup failure quarantines", &reconcilingPublisher{err: errors.New("feed down")}, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outbox, _, run := reconcileBlueskySetup(t, tt.pub)
			if err := run(); err != nil {
				t.Fatal(err)
			}
			if got := len(outbox.delivered) == 1; got != tt.wantDelivered {
				t.Errorf("delivered = %v, want delivered=%v", outbox.delivered, tt.wantDelivered)
			}
			if got := len(outbox.retryable) == 1; got != tt.wantRetryable {
				t.Errorf("retryable = %v, want retryable=%v", outbox.retryable, tt.wantRetryable)
			}
			if got := len(outbox.quarantined) == 1; got != tt.wantQuarantin {
				t.Errorf("quarantined = %v, want quarantined=%v", outbox.quarantined, tt.wantQuarantin)
			}
			if tt.wantDelivered && outbox.sentUnconfirmed[1].SentMessageID != "at://d/p/1" {
				t.Errorf("recorded evidence = %+v, want the matched post URI", outbox.sentUnconfirmed[1])
			}
		})
	}
}

func TestPublishOutbox_ReconcilePassesPayloadAndSkewToDestinationPublisher(t *testing.T) {
	pub := &reconcilingPublisher{covered: true}
	_, _, run := reconcileBlueskySetup(t, pub)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(pub.lookups) != 1 {
		t.Fatalf("lookups = %d, want 1", len(pub.lookups))
	}
	l := pub.lookups[0]
	if l.Payload.Text != "Informe" || l.Article.ID != "a1" || l.ClockSkew != 2*time.Minute || !l.SentAt.Equal(destNow.Add(-time.Minute)) {
		t.Errorf("lookup = %+v", l)
	}
}
