package usecase_test

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// fakeEventHandler matches articles whose title contains match.
type fakeEventHandler struct {
	name    string
	match   string
	handle  func(ctx context.Context, rc out.EventContext, a article.Article) (out.EventResult, error)
	handled atomic.Int32
}

func (h *fakeEventHandler) Name() string { return h.name }
func (h *fakeEventHandler) Interested(a article.Article) bool {
	return strings.Contains(a.Title, h.match)
}
func (h *fakeEventHandler) Handle(ctx context.Context, rc out.EventContext, a article.Article) (out.EventResult, error) {
	h.handled.Add(1)
	if h.handle != nil {
		return h.handle(ctx, rc, a)
	}
	return out.EventResult{Actions: []out.EventAction{out.EnqueuePublication{
		Destination: publishing.DestinationBluesky,
		Payload:     publishing.MessagePayload{Text: "Inactives are out", Kind: "inactives"},
	}}}, nil
}

func eventTestArticle(t *testing.T, id, sourceID, title, url string) article.Article {
	t.Helper()
	return article.Article{
		ID: id, Title: title, Summary: title, OriginalURL: url,
		ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, sourceID), Type: source.TypeRSS},
		PublishedAt: time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC),
	}
}

func eventTestCycle(t *testing.T, handlers []out.EventHandler, enabled bool, timeout time.Duration, articles ...article.Article) (*usecase.ProcessCycle, *sql.DB) {
	t.Helper()
	db := newIntegrationDB(t)
	clock := fakeClock{now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)}
	pc, _ := newIntegrationProcessCycle(db, fakeFetcher{articles: articles}, clock)
	settings := map[string]usecase.EventHandlerSettings{}
	for _, h := range handlers {
		settings[h.Name()] = usecase.EventHandlerSettings{Enabled: enabled}
	}
	d, err := usecase.NewEventDispatcher(handlers, settings, usecase.EventDeps{Clock: clock, Logger: fakeLogger{}}, timeout)
	if err != nil {
		t.Fatalf("NewEventDispatcher() error = %v", err)
	}
	pc.SetEventDispatcher(d)
	return pc, db
}

func destinationOutboxCount(t *testing.T, db *sql.DB, articleID, destination string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM publication_outbox WHERE article_id = ? AND destination = ?`, articleID, destination).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

func articleStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM articles WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatalf("article status: %v", err)
	}
	return s
}

func TestProcessCycle_NoEventHandlers_ReportsNoEventActivity(t *testing.T) {
	db := newIntegrationDB(t)
	a := eventTestArticle(t, "a1", "src-a", "Inactives: Week 7 Isotopes at Sharks", "https://example.com/a1")
	pc, _ := newIntegrationProcessCycle(db, fakeFetcher{articles: []article.Article{a}}, fakeClock{now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)})
	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Events != (in.EventHandlerMetrics{}) {
		t.Errorf("Events = %+v, want zero", result.Events)
	}
	if result.Accepted != 1 || destinationOutboxCount(t, db, "a1", "telegram") != 1 || destinationOutboxCount(t, db, "a1", "bluesky") != 0 {
		t.Errorf("pipeline changed without handlers: accepted=%d", result.Accepted)
	}
}

func TestProcessCycle_EventHandler_EnqueuesPublicationAndLeavesTelegramAlone(t *testing.T) {
	h := &fakeEventHandler{name: "inactives", match: "Inactives"}
	a := eventTestArticle(t, "a1", "src-a", "Inactives: Week 7 Isotopes at Sharks", "https://example.com/a1")
	pc, db := eventTestCycle(t, []out.EventHandler{h}, true, 0, a)

	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Events.Ran != 1 || result.Events.Matched != 1 || result.Events.Enqueued != 1 {
		t.Errorf("Events = %+v, want ran=1 matched=1 enqueued=1", result.Events)
	}
	if got := destinationOutboxCount(t, db, "a1", "bluesky"); got != 1 {
		t.Errorf("bluesky rows = %d, want 1", got)
	}
	if got := destinationOutboxCount(t, db, "a1", "telegram"); got != 1 {
		t.Errorf("telegram rows = %d, want 1 (normal pipeline untouched)", got)
	}
	if result.Accepted != 1 {
		t.Errorf("Accepted = %d, want 1", result.Accepted)
	}
}

func TestProcessCycle_EventHandler_NotInterestedIsNotCalled(t *testing.T) {
	h := &fakeEventHandler{name: "inactives", match: "Inactives"}
	a := eventTestArticle(t, "a1", "src-a", "Isotopes sign a new player", "https://example.com/a1")
	pc, _ := eventTestCycle(t, []out.EventHandler{h}, true, 0, a)
	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if h.handled.Load() != 0 || result.Events.Ran != 0 {
		t.Errorf("Handle called %d times, ran=%d, want 0", h.handled.Load(), result.Events.Ran)
	}
}

func TestProcessCycle_EventHandler_RunsForArticleDedupRejects(t *testing.T) {
	h := &fakeEventHandler{name: "inactives", match: "Inactives"}
	first := eventTestArticle(t, "a1", "src-a", "Inactives: Week 7 Isotopes at Sharks", "https://example.com/same")
	copy2 := eventTestArticle(t, "a2", "src-b", "Inactives: Week 7 Isotopes at Sharks", "https://example.com/same")
	pc, db := eventTestCycle(t, []out.EventHandler{h}, true, 0, first, copy2)

	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate+result.Rejected == 0 && articleStatus(t, db, "a2") != string(article.StatusRejectedDuplicate) {
		t.Fatalf("second copy was not rejected as duplicate: %s", articleStatus(t, db, "a2"))
	}
	if got := articleStatus(t, db, "a2"); got != string(article.StatusRejectedDuplicate) {
		t.Fatalf("a2 status = %s, want %s", got, article.StatusRejectedDuplicate)
	}
	if h.handled.Load() != 2 {
		t.Errorf("Handle called %d times, want 2", h.handled.Load())
	}
	if got := destinationOutboxCount(t, db, "a2", "bluesky"); got != 1 {
		t.Errorf("bluesky rows for the rejected duplicate = %d, want 1", got)
	}
	if got := destinationOutboxCount(t, db, "a2", "telegram"); got != 0 {
		t.Errorf("telegram rows for the rejected duplicate = %d, want 0", got)
	}
}

func TestProcessCycle_EventHandler_DisabledIsNotCalled(t *testing.T) {
	h := &fakeEventHandler{name: "inactives", match: "Inactives"}
	a := eventTestArticle(t, "a1", "src-a", "Inactives: Week 7 Isotopes at Sharks", "https://example.com/a1")
	pc, db := eventTestCycle(t, []out.EventHandler{h}, false, 0, a)
	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if h.handled.Load() != 0 || result.Events.Ran != 0 {
		t.Errorf("disabled handler ran: handled=%d", h.handled.Load())
	}
	if destinationOutboxCount(t, db, "a1", "bluesky") != 0 {
		t.Error("disabled handler enqueued a row")
	}
}

func TestProcessCycle_EventHandler_PanicErrorAndTimeoutAreIsolated(t *testing.T) {
	panicker := &fakeEventHandler{name: "panicker", match: "Inactives", handle: func(context.Context, out.EventContext, article.Article) (out.EventResult, error) {
		panic("boom")
	}}
	failer := &fakeEventHandler{name: "failer", match: "Inactives", handle: func(context.Context, out.EventContext, article.Article) (out.EventResult, error) {
		return out.EventResult{}, context.Canceled
	}}
	slow := &fakeEventHandler{name: "slow", match: "Inactives", handle: func(ctx context.Context, _ out.EventContext, _ article.Article) (out.EventResult, error) {
		<-ctx.Done()
		return out.EventResult{Actions: []out.EventAction{out.EnqueuePublication{Destination: publishing.DestinationBluesky, Payload: publishing.MessagePayload{Text: "late"}}}}, nil
	}}
	good := &fakeEventHandler{name: "good", match: "Inactives"}
	a := eventTestArticle(t, "a1", "src-a", "Inactives: Week 7 Isotopes at Sharks", "https://example.com/a1")
	pc, db := eventTestCycle(t, []out.EventHandler{panicker, failer, slow, good}, true, 20*time.Millisecond, a)

	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	ev := result.Events
	if ev.Panics != 1 || ev.Errors != 1 || ev.Timeouts != 1 || ev.Matched != 1 || ev.Enqueued != 1 {
		t.Errorf("Events = %+v, want panics=1 errors=1 timeouts=1 matched=1 enqueued=1", ev)
	}
	if got := destinationOutboxCount(t, db, "a1", "bluesky"); got != 1 {
		t.Errorf("bluesky rows = %d, want 1 (only the good handler)", got)
	}
	if result.Accepted != 1 || destinationOutboxCount(t, db, "a1", "telegram") != 1 {
		t.Error("article processing was affected by failing handlers")
	}
}

func TestProcessCycle_EventHandler_InvalidActionIsCountedNotFatal(t *testing.T) {
	bad := &fakeEventHandler{name: "bad", match: "Inactives", handle: func(context.Context, out.EventContext, article.Article) (out.EventResult, error) {
		return out.EventResult{Actions: []out.EventAction{out.EnqueuePublication{
			Destination: publishing.DestinationBluesky,
			Payload:     publishing.MessagePayload{Text: "x", Images: []publishing.ImageRef{{Path: "/abs.png", MIME: "image/png"}}},
		}}}, nil
	}}
	a := eventTestArticle(t, "a1", "src-a", "Inactives: Week 7 Isotopes at Sharks", "https://example.com/a1")
	pc, db := eventTestCycle(t, []out.EventHandler{bad}, true, 0, a)
	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Events.Errors != 1 || result.Events.Enqueued != 0 {
		t.Errorf("Events = %+v, want errors=1 enqueued=0", result.Events)
	}
	if result.Accepted != 1 || destinationOutboxCount(t, db, "a1", "telegram") != 1 {
		t.Error("invalid action changed normal processing")
	}
}

func TestNewEventDispatcher_RejectsBadHandlers(t *testing.T) {
	deps := usecase.EventDeps{Clock: fakeClock{}, Logger: fakeLogger{}}
	if _, err := usecase.NewEventDispatcher([]out.EventHandler{nil}, nil, deps, 0); err == nil {
		t.Error("nil handler: want error")
	}
	if _, err := usecase.NewEventDispatcher([]out.EventHandler{&fakeEventHandler{name: " "}}, nil, deps, 0); err == nil {
		t.Error("blank name: want error")
	}
	if _, err := usecase.NewEventDispatcher([]out.EventHandler{&fakeEventHandler{name: "x"}, &fakeEventHandler{name: "x"}}, nil, deps, 0); err == nil {
		t.Error("duplicate name: want error")
	}
}
