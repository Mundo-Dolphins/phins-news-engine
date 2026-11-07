package usecase_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var destNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// destinationOutbox adds the optional destination support to the shared fake.
type destinationOutbox struct {
	*fakeOutboxRepo
	open []publishing.Destination
}

func (d *destinationOutbox) EnqueueDestination(context.Context, string, string, publishing.PublicationLane, publishing.Destination, time.Time) error {
	return nil
}
func (d *destinationOutbox) DestinationOf(context.Context, int64) (publishing.Destination, error) {
	return publishing.DestinationBluesky, nil
}
func (d *destinationOutbox) RecordThreadPart(context.Context, int64, string, time.Time) error {
	return nil
}
func (d *destinationOutbox) ResumeThread(context.Context, int64, string, time.Time) error {
	return nil
}
func (d *destinationOutbox) OpenDestinations(context.Context) ([]publishing.Destination, error) {
	return d.open, nil
}

func twoDestinationSetup(t *testing.T, tgPub, bskyPub *fakePublisher) (*fakeOutboxRepo, *fakeArticleRepo, *fakeClusterPublicationRepository, func() error) {
	t.Helper()
	articles := &fakeArticleRepo{}
	if err := articles.Save(context.Background(), article.Article{ID: "a1", Status: article.StatusPendingPublication, ClusterID: "c1"}); err != nil {
		t.Fatal(err)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{
		{ID: 1, ArticleID: "a1", Destination: publishing.DestinationTelegram},
		{ID: 2, ArticleID: "a1", Destination: publishing.DestinationBluesky},
	}}
	gate := &fakeClusterPublicationRepository{}
	uc := newCountingPublishOutbox(articles, outbox, tgPub, gate)
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, bskyPub); err != nil {
		t.Fatal(err)
	}
	run := func() error { _, err := uc.Run(context.Background()); return err }
	return outbox, articles, gate, run
}

func TestPublishOutbox_RoutesEachRowToItsOwnPublisher(t *testing.T) {
	tg, bsky := &fakePublisher{messageID: "tg-1"}, &fakePublisher{messageID: "bsky-1"}
	outbox, _, _, run := twoDestinationSetup(t, tg, bsky)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(tg.published) != 1 || len(bsky.published) != 1 {
		t.Fatalf("telegram=%d bluesky=%d publishes, want 1 each", len(tg.published), len(bsky.published))
	}
	if len(outbox.delivered) != 2 {
		t.Fatalf("delivered = %v, want both rows", outbox.delivered)
	}
	ids := map[int64]string{}
	for _, a := range outbox.attempts {
		ids[a.OutboxID] = a.ExternalMessageID
	}
	if ids[1] != "tg-1" || ids[2] != "bsky-1" {
		t.Fatalf("external ids by outbox row = %v", ids)
	}
}

func TestPublishOutbox_ResultIsBrokenDownByDestination(t *testing.T) {
	tg, bsky := &fakePublisher{}, &fakePublisher{}
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{
		{ID: 1, ArticleID: "a1"}, // zero destination == telegram
		{ID: 2, ArticleID: "a1", Destination: publishing.DestinationBluesky},
	}}
	uc := newCountingPublishOutbox(articles, outbox, tg, &fakeClusterPublicationRepository{})
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, bsky); err != nil {
		t.Fatal(err)
	}
	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []publishing.Destination{publishing.DestinationTelegram, publishing.DestinationBluesky} {
		if c := result.ByDestination[d]; c == nil || c.Claimed != 1 || c.Published != 1 {
			t.Errorf("ByDestination[%s] = %+v, want claimed=1 published=1", d, c)
		}
	}
}

func TestPublishOutbox_SecondaryFailureDoesNotAffectTelegramOrArticle(t *testing.T) {
	tg, bsky := &fakePublisher{}, &fakePublisher{err: errors.New("bluesky down"), permanent: true}
	outbox, articles, gate, run := twoDestinationSetup(t, tg, bsky)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 1 {
		t.Fatalf("delivered = %v, want only the telegram row", outbox.delivered)
	}
	if len(outbox.discarded) != 1 || outbox.discarded[0] != 2 {
		t.Fatalf("discarded = %v, want only the bluesky row", outbox.discarded)
	}
	if got := articles.lastSaved(); got.Status != article.StatusPublished {
		t.Fatalf("article status = %v, want published (a bluesky failure must not discard it)", got.Status)
	}
	if len(gate.released) != 0 {
		t.Fatalf("cluster gate released %v by a bluesky failure", gate.released)
	}
	if len(gate.confirmed) != 1 {
		t.Fatalf("cluster gate confirmed %v, want exactly the telegram delivery", gate.confirmed)
	}
}

func TestPublishOutbox_TelegramFailureDoesNotAffectSecondary(t *testing.T) {
	tg, bsky := &fakePublisher{err: errors.New("telegram down")}, &fakePublisher{}
	outbox, _, _, run := twoDestinationSetup(t, tg, bsky)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(outbox.retryable) != 1 || outbox.retryable[0] != 1 {
		t.Fatalf("retryable = %v, want telegram row only", outbox.retryable)
	}
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 2 {
		t.Fatalf("delivered = %v, want bluesky row only", outbox.delivered)
	}
}

func TestPublishOutbox_TelegramRateLimitOnlyDefersTelegramRows(t *testing.T) {
	tg := &fakePublisher{err: &out.RateLimitedError{Err: errors.New("429"), RetryAfter: 5 * time.Minute}}
	bsky := &fakePublisher{}
	articles := &fakeArticleRepo{}
	for _, id := range []string{"a1", "a2", "a3"} {
		seedPendingArticle(t, articles, id)
	}
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{
		{ID: 1, ArticleID: "a1"},
		{ID: 2, ArticleID: "a2", Destination: publishing.DestinationBluesky},
		{ID: 3, ArticleID: "a3"},
	}}
	uc := newCountingPublishOutbox(articles, outbox, tg, &fakeClusterPublicationRepository{})
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, bsky); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(outbox.deferred) != 1 || outbox.deferred[0] != 3 {
		t.Fatalf("deferred = %v, want [3]", outbox.deferred)
	}
	if len(bsky.published) != 1 {
		t.Fatalf("bluesky publishes = %d, want 1 (a Telegram 429 must not stop it)", len(bsky.published))
	}
}

// countingOutbox reports per-destination success counts, as the SQLite
// repository does.
type countingOutbox struct {
	*fakeOutboxRepo
	burst map[publishing.Destination]int
}

func (c *countingOutbox) CountSucceededSinceDestination(_ context.Context, d publishing.Destination, _ publishing.PublicationLane, _ time.Time) (int, error) {
	return c.burst[d], nil
}
func (c *countingOutbox) CountSucceededByPrimaryEntitySinceDestination(context.Context, publishing.Destination, publishing.PublicationLane, string, time.Time) (int, error) {
	return 0, nil
}

func TestPublishOutbox_BurstLimitIsCountedAndAppliedPerDestination(t *testing.T) {
	tg, bsky := &fakePublisher{}, &fakePublisher{}
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	seedPendingArticle(t, articles, "a2")
	base := &fakeOutboxRepo{claimItems: []out.OutboxItem{
		{ID: 1, ArticleID: "a1"},
		{ID: 2, ArticleID: "a2", Destination: publishing.DestinationBluesky},
	}}
	// Telegram is at its limit; Bluesky has sent nothing.
	outbox := &countingOutbox{fakeOutboxRepo: base, burst: map[publishing.Destination]int{publishing.DestinationTelegram: 5}}
	policy := publishing.PolicyConfig{BurstMaxPerWindow: 5, BurstWindow: time.Hour}
	uc := usecase.NewPublishOutbox(articles, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, tg, testBackoff, policy, "test-worker", 10, time.Minute, fakeClock{now: destNow}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10}, nil)
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, bsky); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(base.deferred) != 1 || base.deferred[0] != 1 || len(tg.published) != 0 {
		t.Fatalf("deferred=%v telegram publishes=%d; telegram must be deferred", base.deferred, len(tg.published))
	}
	if len(bsky.published) != 1 {
		t.Fatalf("bluesky publishes=%d; its own count is 0 so it must publish", len(bsky.published))
	}

	// Now Bluesky is at its limit too: it is deferred on its own count.
	outbox.burst[publishing.DestinationBluesky] = 5
	base.claimItems = []out.OutboxItem{{ID: 2, ArticleID: "a2", Destination: publishing.DestinationBluesky}}
	base.deferred = nil
	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(base.deferred) != 1 || base.deferred[0] != 2 || len(bsky.published) != 1 {
		t.Fatalf("deferred=%v bluesky publishes=%d; bluesky must defer at its limit", base.deferred, len(bsky.published))
	}
}

func TestPublishOutbox_DestinationWithoutCountSupportFailsLoudly(t *testing.T) {
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 2, ArticleID: "a1", Destination: publishing.DestinationBluesky}}}
	uc := newPublishOutboxWithPolicy(articles, outbox, &fakePublisher{}, allowAllPolicy)
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, &fakePublisher{}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Run(context.Background()); err == nil {
		t.Fatal("Run() succeeded although sends cannot be counted per destination")
	}
}

func TestPublishOutbox_UnregisteredDestinationFailsLoudly(t *testing.T) {
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{{ID: 2, ArticleID: "a1", Destination: publishing.DestinationBluesky}}}
	uc := newPublishOutbox(articles, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: destNow}, &fakeClusterPublicationRepository{})
	_, err := uc.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no publisher is registered") {
		t.Fatalf("Run() error = %v, want unregistered-destination error", err)
	}
}

func TestPublishOutbox_RegisterPublisherValidation(t *testing.T) {
	uc := newPublishOutbox(&fakeArticleRepo{}, &fakeOutboxRepo{}, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: destNow}, &fakeClusterPublicationRepository{})
	if err := uc.RegisterPublisher(publishing.DestinationTelegram, &fakePublisher{}); err == nil {
		t.Error("replacing the telegram publisher succeeded")
	}
	if err := uc.RegisterPublisher("mastodon", &fakePublisher{}); err == nil {
		t.Error("unknown destination accepted")
	}
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, nil); err == nil {
		t.Error("nil publisher accepted")
	}
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, &fakePublisher{}); err != nil {
		t.Fatal(err)
	}
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, &fakePublisher{}); err == nil {
		t.Error("duplicate registration accepted")
	}
}

func TestPublishOutbox_ValidateWiring(t *testing.T) {
	outbox := &destinationOutbox{fakeOutboxRepo: &fakeOutboxRepo{}, open: []publishing.Destination{publishing.DestinationTelegram, publishing.DestinationBluesky}}
	uc := newPublishOutbox(&fakeArticleRepo{}, outbox.fakeOutboxRepo, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, fakeClock{now: destNow}, &fakeClusterPublicationRepository{})
	// The shared fake has no destination support: nothing to validate.
	if err := uc.ValidateWiring(context.Background()); err != nil {
		t.Fatalf("ValidateWiring without destination support: %v", err)
	}
	uc = newDestinationPublishOutbox(outbox)
	if err := uc.ValidateWiring(context.Background()); err == nil {
		t.Fatal("ValidateWiring accepted open bluesky rows with no publisher")
	}
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, &fakePublisher{}); err != nil {
		t.Fatal(err)
	}
	if err := uc.ValidateWiring(context.Background()); err != nil {
		t.Fatalf("ValidateWiring after registering: %v", err)
	}
}

func TestPublishOutbox_ReconcileNeverUsesTelegramHistoryForOtherDestinations(t *testing.T) {
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	seedPendingArticle(t, articles, "a2")
	outbox := &fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{
		{ID: 1, ArticleID: "a1", Destination: publishing.DestinationBluesky, SentMessageID: "at://post/1"},
		{ID: 2, ArticleID: "a2", Destination: publishing.DestinationBluesky}, // no evidence
	}}
	importer := &fakeChannelImporter{}
	uc := newPublishOutboxWithImporter(articles, outbox, importer)
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, &fakePublisher{}); err != nil {
		t.Fatal(err)
	}
	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if importer.calls != 0 {
		t.Fatalf("telegram history fetched %d times for bluesky rows", importer.calls)
	}
	if result.ReconciledPositive != 1 || result.Quarantined != 1 {
		t.Fatalf("result = %+v, want 1 positive (local evidence) and 1 quarantined", result)
	}
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 1 || len(outbox.quarantined) != 1 || outbox.quarantined[0] != 2 {
		t.Fatalf("delivered=%v quarantined=%v", outbox.delivered, outbox.quarantined)
	}
	if got, _ := articles.FindByID(context.Background(), "a1"); got.Status != article.StatusPendingPublication {
		t.Fatalf("reconciling a bluesky row changed the article to %v", got.Status)
	}
}

func newPublishOutboxWithPolicy(articles *fakeArticleRepo, outbox *fakeOutboxRepo, pub *fakePublisher, policy publishing.PolicyConfig) *usecase.PublishOutbox {
	return usecase.NewPublishOutbox(articles, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, policy, "test-worker", 10, time.Minute, fakeClock{now: destNow}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
}

func newPublishOutboxWithImporter(articles *fakeArticleRepo, outbox *fakeOutboxRepo, importer out.TelegramChannelImporter) *usecase.PublishOutbox {
	return usecase.NewPublishOutbox(articles, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: destNow}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, importer)
}

func newDestinationPublishOutbox(outbox *destinationOutbox) *usecase.PublishOutbox {
	return usecase.NewPublishOutbox(&fakeArticleRepo{}, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: destNow}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10}, nil)
}

// newCountingPublishOutbox wires the use case over an outbox that can count
// sends per destination (all zero), as the SQLite repository can.
func newCountingPublishOutbox(articles *fakeArticleRepo, base *fakeOutboxRepo, pub *fakePublisher, gate *fakeClusterPublicationRepository) *usecase.PublishOutbox {
	outbox := &countingOutbox{fakeOutboxRepo: base}
	return usecase.NewPublishOutbox(articles, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, pub, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: destNow}, fakeLogger{}, gate, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10}, nil)
}
