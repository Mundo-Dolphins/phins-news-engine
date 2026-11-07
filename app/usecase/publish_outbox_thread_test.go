package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// threadOutbox is a per-destination outbox that records thread progress.
type threadOutbox struct {
	*countingOutbox
	progress map[int64]string // latest progress written by RecordThreadPart or ResumeThread
	resumed  []int64
}

func newThreadOutbox(base *fakeOutboxRepo) *threadOutbox {
	return &threadOutbox{countingOutbox: &countingOutbox{fakeOutboxRepo: base}, progress: map[int64]string{}}
}

func (o *threadOutbox) EnqueueDestination(context.Context, string, string, publishing.PublicationLane, publishing.Destination, time.Time) error {
	return nil
}
func (o *threadOutbox) DestinationOf(context.Context, int64) (publishing.Destination, error) {
	return publishing.DestinationBluesky, nil
}
func (o *threadOutbox) OpenDestinations(context.Context) ([]publishing.Destination, error) {
	return nil, nil
}
func (o *threadOutbox) RecordThreadPart(_ context.Context, id int64, progress string, _ time.Time) error {
	o.progress[id] = progress
	return nil
}
func (o *threadOutbox) ResumeThread(_ context.Context, id int64, progress string, _ time.Time) error {
	o.progress[id] = progress
	o.resumed = append(o.resumed, id)
	return nil
}

func (o *threadOutbox) refs(t *testing.T, id int64) []publishing.PostRef {
	t.Helper()
	refs, err := publishing.ParseThreadProgress(o.progress[id])
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

// threadPublisher posts each missing part as at://t/p/<index>, failing at
// part failAt with failErr when failErr is set.
type threadPublisher struct {
	payloadPublisher
	failAt  int
	failErr error
	dones   [][]publishing.PostRef
	posted  []string
}

func (p *threadPublisher) PublishThread(_ context.Context, _ article.Article, payload publishing.MessagePayload,
	done []publishing.PostRef, record func(publishing.PostRef) error) (string, error) {
	p.dones = append(p.dones, append([]publishing.PostRef(nil), done...))
	refs := append([]publishing.PostRef(nil), done...)
	parts := payload.Parts()
	for i := len(done); i < len(parts); i++ {
		if p.failErr != nil && i == p.failAt {
			return "", p.failErr
		}
		ref := threadRef(i)
		p.posted = append(p.posted, parts[i])
		if err := record(ref); err != nil {
			return "", err
		}
		refs = append(refs, ref)
	}
	return refs[0].URI, nil
}

// threadReconciler is a threadPublisher that also looks up its history.
type threadReconciler struct {
	threadPublisher
	ref     publishing.PostRef
	matched bool
	covered bool
	lookups []out.SentLookup
}

func (r *threadReconciler) FindSent(_ context.Context, l out.SentLookup) (publishing.PostRef, bool, bool, error) {
	r.lookups = append(r.lookups, l)
	return r.ref, r.matched, r.covered, nil
}

func threadRef(i int) publishing.PostRef {
	return publishing.PostRef{URI: fmt.Sprintf("at://t/p/%d", i), CID: fmt.Sprintf("c%d", i)}
}

func threadProgress(t *testing.T, n int) string {
	t.Helper()
	refs := []publishing.PostRef{}
	for i := 0; i < n; i++ {
		refs = append(refs, threadRef(i))
	}
	raw, err := publishing.MarshalThreadProgress(refs)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func threadPayload(t *testing.T, continuations ...string) string {
	t.Helper()
	raw, err := publishing.MessagePayload{Text: "parte uno", Continuations: continuations}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newThreadPublishOutbox(t *testing.T, outbox *threadOutbox, pub out.Publisher, alerter out.Alerter) *usecase.PublishOutbox {
	t.Helper()
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	uc := usecase.NewPublishOutbox(articles, outbox, &fakeDecisionRepo{}, &fakeUnitOfWork{}, &fakePublisher{}, testBackoff, allowAllPolicy, "test-worker", 10, time.Minute, fakeClock{now: destNow}, fakeLogger{}, &fakeClusterPublicationRepository{}, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ClockSkew: 2 * time.Minute}, nil)
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, pub); err != nil {
		t.Fatal(err)
	}
	if alerter != nil {
		uc.SetAlerter(alerter)
	}
	return uc
}

func runThreadRow(t *testing.T, item out.OutboxItem, pub out.Publisher, alerter out.Alerter) *threadOutbox {
	t.Helper()
	outbox := newThreadOutbox(&fakeOutboxRepo{claimItems: []out.OutboxItem{item}})
	if _, err := newThreadPublishOutbox(t, outbox, pub, alerter).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return outbox
}

func threadItem(payload, progress string) out.OutboxItem {
	return out.OutboxItem{ID: 2, ArticleID: "a1", Destination: publishing.DestinationBluesky, Payload: payload, ThreadProgress: progress}
}

func TestPublishOutbox_ThreadDelivered(t *testing.T) {
	pub := &threadPublisher{}
	outbox := runThreadRow(t, threadItem(threadPayload(t, "parte dos"), "[]"), pub, nil)
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 2 {
		t.Fatalf("delivered = %v, want the thread row", outbox.delivered)
	}
	if got := outbox.sentUnconfirmed[2].SentMessageID; got != "at://t/p/0" {
		t.Errorf("recorded message id = %q, want the first part's", got)
	}
	if refs := outbox.refs(t, 2); len(refs) != 2 || refs[1] != threadRef(1) {
		t.Errorf("thread progress = %+v, want 2 refs", refs)
	}
}

func TestPublishOutbox_ThreadResumesFromProgress(t *testing.T) {
	pub := &threadPublisher{}
	outbox := runThreadRow(t, threadItem(threadPayload(t, "parte dos"), threadProgress(t, 1)), pub, nil)
	if len(pub.dones) != 1 || len(pub.dones[0]) != 1 || pub.dones[0][0] != threadRef(0) {
		t.Fatalf("done = %+v, want the one posted part", pub.dones)
	}
	if len(pub.posted) != 1 || pub.posted[0] != "parte dos" {
		t.Fatalf("posted = %v, want only part 1", pub.posted)
	}
	if len(outbox.delivered) != 1 {
		t.Fatalf("delivered = %v, want the thread row", outbox.delivered)
	}
}

func TestPublishOutbox_ThreadTemporaryFailureKeepsProgress(t *testing.T) {
	pub := &threadPublisher{failAt: 1, failErr: errors.New("network down")}
	outbox := runThreadRow(t, threadItem(threadPayload(t, "parte dos"), "[]"), pub, nil)
	if len(outbox.retryable) != 1 || outbox.retryable[0] != 2 {
		t.Fatalf("retryable = %v, want the thread row", outbox.retryable)
	}
	if refs := outbox.refs(t, 2); len(refs) != 1 || refs[0] != threadRef(0) {
		t.Errorf("thread progress = %+v, want the 1 posted part", refs)
	}
}

func TestPublishOutbox_ThreadUnsupportedDestination(t *testing.T) {
	pub := &payloadPublisher{}
	outbox := runThreadRow(t, threadItem(threadPayload(t, "parte dos"), "[]"), pub, nil)
	if len(pub.payloads) != 0 || len(pub.published) != 0 {
		t.Fatalf("published %+v / %+v, want nothing", pub.payloads, pub.published)
	}
	if len(outbox.discarded) != 1 || outbox.discarded[0] != 2 {
		t.Fatalf("discarded = %v, want the thread row dead", outbox.discarded)
	}
	if len(outbox.attempts) != 1 || !strings.Contains(outbox.attempts[0].ErrorMessage, "does not support threads") {
		t.Errorf("attempts = %+v", outbox.attempts)
	}
}

func reconcileThreadRow(t *testing.T, pub *threadReconciler, payload, progress string) *threadOutbox {
	t.Helper()
	outbox := newThreadOutbox(&fakeOutboxRepo{sentUnconfirmedList: []out.OutboxItem{{
		ID: 1, ArticleID: "a1", Destination: publishing.DestinationBluesky, Payload: payload,
		ThreadProgress: progress, SentAt: destNow.Add(-time.Minute),
	}}})
	if _, err := newThreadPublishOutbox(t, outbox, pub, nil).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return outbox
}

func TestReconcile_ThreadPartFoundContinues(t *testing.T) {
	pub := &threadReconciler{ref: threadRef(1), matched: true, covered: true}
	outbox := reconcileThreadRow(t, pub, threadPayload(t, "parte dos", "parte tres"), threadProgress(t, 1))
	if len(pub.lookups) != 1 || len(pub.lookups[0].ThreadProgress) != 1 || len(pub.lookups[0].Payload.Continuations) != 2 {
		t.Fatalf("lookups = %+v, want the payload and its 1-part progress", pub.lookups)
	}
	if len(outbox.resumed) != 1 || outbox.resumed[0] != 1 {
		t.Fatalf("resumed = %v, want the thread row back to retryable", outbox.resumed)
	}
	if refs := outbox.refs(t, 1); len(refs) != 2 || refs[1] != threadRef(1) {
		t.Errorf("thread progress = %+v, want 2 refs", refs)
	}
	if len(outbox.delivered) != 0 || len(outbox.quarantined) != 0 {
		t.Errorf("delivered=%v quarantined=%v", outbox.delivered, outbox.quarantined)
	}
}

func TestReconcile_ThreadLastPartFoundDelivered(t *testing.T) {
	pub := &threadReconciler{ref: threadRef(1), matched: true, covered: true}
	outbox := reconcileThreadRow(t, pub, threadPayload(t, "parte dos"), threadProgress(t, 1))
	if len(outbox.delivered) != 1 || outbox.delivered[0] != 1 {
		t.Fatalf("delivered = %v, want the thread row", outbox.delivered)
	}
	if got := outbox.sentUnconfirmed[1].SentMessageID; got != "at://t/p/0" {
		t.Errorf("recorded message id = %q, want the root's URI", got)
	}
	if len(outbox.resumed) != 0 {
		t.Errorf("resumed = %v, want none", outbox.resumed)
	}
}

func TestReconcile_ThreadPartNotFoundResendsIt(t *testing.T) {
	pub := &threadReconciler{covered: true}
	outbox := reconcileThreadRow(t, pub, threadPayload(t, "parte dos"), threadProgress(t, 1))
	if len(outbox.resumed) != 1 || outbox.resumed[0] != 1 {
		t.Fatalf("resumed = %v, want the thread row back to retryable", outbox.resumed)
	}
	if refs := outbox.refs(t, 1); len(refs) != 1 {
		t.Errorf("thread progress = %+v, want it unchanged", refs)
	}
	if len(outbox.retryable) != 0 {
		t.Errorf("retryable = %v: a thread row must resume, not restart", outbox.retryable)
	}
}

func TestReconcile_ThreadInconclusiveQuarantines(t *testing.T) {
	pub := &threadReconciler{}
	outbox := reconcileThreadRow(t, pub, threadPayload(t, "parte dos"), threadProgress(t, 1))
	if len(outbox.quarantined) != 1 || len(outbox.resumed) != 0 {
		t.Fatalf("quarantined=%v resumed=%v, want quarantine only", outbox.quarantined, outbox.resumed)
	}
}

func TestAlert_ThreadPartialDead(t *testing.T) {
	alerter := &recordingAlerter{}
	pub := &threadPublisher{failAt: 1, failErr: &out.PermanentError{Err: errors.New("rejected")}}
	outbox := runThreadRow(t, threadItem(threadPayload(t, "parte dos"), "[]"), pub, alerter)
	if len(outbox.discarded) != 1 {
		t.Fatalf("discarded = %v, want the thread row dead", outbox.discarded)
	}
	if len(alerter.alerts) != 1 {
		t.Fatalf("alerts = %+v, want one", alerter.alerts)
	}
	details := alerter.alerts[0].Details
	for _, want := range []string{"posted parts: 1/2", "last: at://t/p/0", "parte dos"} {
		if !strings.Contains(details, want) {
			t.Errorf("details %q lack %q", details, want)
		}
	}
}
