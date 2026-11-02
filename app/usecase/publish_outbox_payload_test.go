package usecase_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// payloadPublisher is a fakePublisher that also implements out.PayloadPublisher.
type payloadPublisher struct {
	fakePublisher
	payloads []publishing.MessagePayload
}

func (p *payloadPublisher) PublishPayload(_ context.Context, a article.Article, payload publishing.MessagePayload) (string, error) {
	p.payloads = append(p.payloads, payload)
	return p.fakePublisher.Publish(context.Background(), a)
}

func runPayloadRow(t *testing.T, raw string, bsky out.Publisher) (*fakeOutboxRepo, error) {
	t.Helper()
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{
		{ID: 2, ArticleID: "a1", Destination: publishing.DestinationBluesky, Payload: raw},
	}}
	uc := newCountingPublishOutbox(articles, outbox, &fakePublisher{}, &fakeClusterPublicationRepository{})
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, bsky); err != nil {
		t.Fatal(err)
	}
	_, err := uc.Run(context.Background())
	return outbox, err
}

func TestPublishOutbox_PassesPayloadToPayloadPublisher(t *testing.T) {
	pub := &payloadPublisher{}
	want := publishing.MessagePayload{
		Text:   "Primer injury report de la semana 8",
		Images: []publishing.ImageRef{{Path: "reports/w8.png", MIME: "image/png", Alt: "Injury report"}},
		Kind:   "injury_report",
	}
	raw, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := runPayloadRow(t, raw, pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.payloads) != 1 || !reflect.DeepEqual(pub.payloads[0], want) {
		t.Fatalf("publisher received %+v, want %+v", pub.payloads, want)
	}
	if len(outbox.delivered) != 1 {
		t.Fatalf("delivered = %v, want the row delivered", outbox.delivered)
	}
}

func TestPublishOutbox_LegacyPayloadReachesPayloadPublisherAsEmpty(t *testing.T) {
	for _, raw := range []string{"{}", `{"title":"T","canonical_url":"https://x.test/a"}`} {
		pub := &payloadPublisher{}
		if _, err := runPayloadRow(t, raw, pub); err != nil {
			t.Fatal(err)
		}
		if len(pub.payloads) != 1 || !pub.payloads[0].IsEmpty() {
			t.Fatalf("payload %q: publisher received %+v, want one empty payload", raw, pub.payloads)
		}
	}
}

func TestPublishOutbox_PlainPublisherIgnoresPayload(t *testing.T) {
	pub := &fakePublisher{}
	if _, err := runPayloadRow(t, `{"text":"hi"}`, pub); err != nil {
		t.Fatal(err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("published = %d, want 1 through the legacy Publish path", len(pub.published))
	}
}

func TestPublishOutbox_MalformedPayloadIsPermanentFailure(t *testing.T) {
	pub := &payloadPublisher{}
	outbox, err := runPayloadRow(t, "not json", pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.payloads) != 0 {
		t.Fatalf("publisher was called with a malformed payload: %+v", pub.payloads)
	}
	if len(outbox.discarded) != 1 || outbox.discarded[0] != 2 {
		t.Fatalf("discarded = %v, want the row discarded", outbox.discarded)
	}
}

func TestPublishOutbox_PayloadPublisherErrorsKeepSemantics(t *testing.T) {
	pub := &payloadPublisher{fakePublisher: fakePublisher{err: errors.New("rejected"), permanent: true}}
	outbox, err := runPayloadRow(t, `{"text":"hi"}`, pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox.discarded) != 1 {
		t.Fatalf("discarded = %v, want the PermanentError to discard the row", outbox.discarded)
	}
}
