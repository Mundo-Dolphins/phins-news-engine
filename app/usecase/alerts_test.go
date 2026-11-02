package usecase_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type recordingAlerter struct {
	mu     sync.Mutex
	alerts []out.Alert
	err    error
}

func (r *recordingAlerter) Alert(_ context.Context, a out.Alert) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, a)
	return r.err
}

func twoDestinationSetupWithAlerter(t *testing.T, tg, bsky *fakePublisher, alerter out.Alerter) func() error {
	t.Helper()
	articles := &fakeArticleRepo{}
	seedPendingArticle(t, articles, "a1")
	outbox := &fakeOutboxRepo{claimItems: []out.OutboxItem{
		{ID: 1, ArticleID: "a1", Destination: publishing.DestinationTelegram},
		{ID: 2, ArticleID: "a1", Destination: publishing.DestinationBluesky},
	}}
	uc := newCountingPublishOutbox(articles, outbox, tg, &fakeClusterPublicationRepository{})
	if err := uc.RegisterPublisher(publishing.DestinationBluesky, bsky); err != nil {
		t.Fatal(err)
	}
	uc.SetAlerter(alerter)
	return func() error { _, err := uc.Run(context.Background()); return err }
}

func TestPublishOutbox_AlertsWhenSecondaryRowGoesDead(t *testing.T) {
	alerter := &recordingAlerter{}
	run := twoDestinationSetupWithAlerter(t, &fakePublisher{}, &fakePublisher{err: errors.New("bluesky rejected"), permanent: true}, alerter)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(alerter.alerts) != 1 {
		t.Fatalf("alerts = %+v, want exactly one (for the bluesky row)", alerter.alerts)
	}
	a := alerter.alerts[0]
	if a.Source != "outbox" || a.Severity != out.AlertCritical {
		t.Errorf("alert = %+v", a)
	}
	for _, want := range []string{"bluesky", "a1", "bluesky rejected"} {
		if !strings.Contains(a.Title+a.Details, want) {
			t.Errorf("alert %q / %q lacks %q", a.Title, a.Details, want)
		}
	}
	if !strings.Contains(a.DedupKey, "bluesky") || !strings.Contains(a.DedupKey, "dead") {
		t.Errorf("DedupKey = %q", a.DedupKey)
	}
}

func TestPublishOutbox_NoAlertForTelegramDeadOrRetryableFailures(t *testing.T) {
	alerter := &recordingAlerter{}
	run := twoDestinationSetupWithAlerter(t, &fakePublisher{err: errors.New("telegram rejected"), permanent: true},
		&fakePublisher{err: errors.New("temporary")}, alerter)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(alerter.alerts) != 0 {
		t.Fatalf("alerts = %+v, want none (telegram is the news channel; the bluesky row is only retrying)", alerter.alerts)
	}
}

func TestPublishOutbox_AlertFailureDoesNotFailTheRun(t *testing.T) {
	alerter := &recordingAlerter{err: errors.New("telegram unreachable")}
	run := twoDestinationSetupWithAlerter(t, &fakePublisher{}, &fakePublisher{err: errors.New("x"), permanent: true}, alerter)
	if err := run(); err != nil {
		t.Fatalf("Run() error = %v, want the alert failure only logged", err)
	}
	if len(alerter.alerts) != 1 {
		t.Fatalf("alerts = %d, want the alert attempted once", len(alerter.alerts))
	}
}

func TestEventAlertAdapter_RaisesDedupedAlertPerHandlerAndMessage(t *testing.T) {
	alerter := &recordingAlerter{}
	sink := usecase.NewEventAlertAdapter(alerter, fakeLogger{})
	sink.Alert(context.Background(), "weekly", "could not read the week")
	sink.Alert(context.Background(), "weekly", "could not read the week")
	sink.Alert(context.Background(), "other", "could not read the week")
	if len(alerter.alerts) != 3 {
		t.Fatalf("alerts = %d, want every call forwarded (the Alerter owns the window)", len(alerter.alerts))
	}
	a, b, c := alerter.alerts[0], alerter.alerts[1], alerter.alerts[2]
	if a.DedupKey != b.DedupKey {
		t.Errorf("same handler and message: keys %q != %q", a.DedupKey, b.DedupKey)
	}
	if a.DedupKey == c.DedupKey {
		t.Errorf("different handlers share key %q", a.DedupKey)
	}
	if a.Source != "weekly" || !strings.Contains(a.Details, "could not read the week") {
		t.Errorf("alert = %+v", a)
	}
}

func TestEventAlertAdapter_AlerterFailureIsSwallowed(t *testing.T) {
	sink := usecase.NewEventAlertAdapter(&recordingAlerter{err: errors.New("down")}, fakeLogger{})
	sink.Alert(context.Background(), "weekly", "boom") // must not panic or block
}
