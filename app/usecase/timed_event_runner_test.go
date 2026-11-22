package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// stepClock is a clock tests move forward by hand.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type recordingEventAlerts struct {
	mu       sync.Mutex
	messages []string
}

func (r *recordingEventAlerts) Alert(_ context.Context, handler, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, handler+": "+message)
}

func (r *recordingEventAlerts) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

// fakeTimedHandler reports a fixed schedule and counts its calls.
type fakeTimedHandler struct {
	name   string
	events []out.DueEvent
	handle func(ctx context.Context, ev out.DueEvent) (out.EventResult, error)
	duePan bool
	calls  int
}

func (h *fakeTimedHandler) Name() string { return h.name }
func (h *fakeTimedHandler) Due(time.Time) []out.DueEvent {
	if h.duePan {
		panic("boom in Due")
	}
	return h.events
}
func (h *fakeTimedHandler) Handle(ctx context.Context, _ out.EventContext, ev out.DueEvent) (out.EventResult, error) {
	h.calls++
	if h.handle != nil {
		return h.handle(ctx, ev)
	}
	return out.EventResult{Actions: []out.EventAction{out.EnqueuePublication{
		Destination: publishing.DestinationBluesky,
		Payload:     publishing.MessagePayload{Text: "Forecast: sunny", Kind: "weather"},
	}}}, nil
}

type timedFixture struct {
	db     *sql.DB
	clock  *stepClock
	alerts *recordingEventAlerts
}

var kickoff = time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)

func newTimedFixture(t *testing.T) *timedFixture {
	t.Helper()
	return &timedFixture{
		db:     newIntegrationDB(t),
		clock:  &stepClock{now: kickoff.Add(-2 * time.Hour)},
		alerts: &recordingEventAlerts{},
	}
}

// runner builds a fresh runner over the fixture's database, which is what a
// restart looks like.
func (f *timedFixture) runner(t *testing.T, enabled bool, grace time.Duration, handlers ...out.TimedEventHandler) *usecase.TimedEventRunner {
	t.Helper()
	settings := map[string]usecase.EventHandlerSettings{}
	for _, h := range handlers {
		settings[h.Name()] = usecase.EventHandlerSettings{Enabled: enabled}
	}
	outbox := sqlite.NewOutboxRepository(f.db)
	r, err := usecase.NewTimedEventRunner(handlers, nil, settings,
		usecase.EventDeps{Clock: f.clock, Logger: fakeLogger{}, Claims: sqlite.NewEventClaimRepository(f.db), Alerts: f.alerts},
		sqlite.NewArticleRepository(f.db), outbox, sqlite.NewUnitOfWork(f.db), 200*time.Millisecond, grace)
	if err != nil {
		t.Fatalf("NewTimedEventRunner() error = %v", err)
	}
	return r
}

func (f *timedFixture) outboxRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM publication_outbox`).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

func (f *timedFixture) claimState(t *testing.T, handler, key string) (state string, found bool) {
	t.Helper()
	err := f.db.QueryRow(`SELECT state FROM event_claims WHERE handler = ? AND claim_key = ?`, handler, key).Scan(&state)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("claim state: %v", err)
	}
	return state, true
}

func dueAtKickoffMinusHour(key string) []out.DueEvent {
	return []out.DueEvent{{Key: key, At: kickoff.Add(-time.Hour)}}
}

func TestTimedEventRunner_DoesNotFireBeforeItsInstantAndFiresOnceAtIt(t *testing.T) {
	f := newTimedFixture(t)
	h := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("2026-reg-1")}
	r := f.runner(t, true, 15*time.Minute, h)

	res, err := r.Run(context.Background())
	if err != nil || h.calls != 0 || res.Ran != 0 || f.outboxRows(t) != 0 {
		t.Fatalf("before the instant: err=%v calls=%d res=%+v rows=%d", err, h.calls, res, f.outboxRows(t))
	}

	f.clock.Set(kickoff.Add(-time.Hour))
	res, err = r.Run(context.Background())
	if err != nil || h.calls != 1 || res.Enqueued != 1 || f.outboxRows(t) != 1 {
		t.Fatalf("at the instant: err=%v calls=%d res=%+v rows=%d", err, h.calls, res, f.outboxRows(t))
	}
	if state, found := f.claimState(t, "weather", "2026-reg-1"); !found || state != "claimed" {
		t.Fatalf("claim = %q found=%v, want claimed", state, found)
	}

	f.clock.Set(kickoff.Add(-time.Hour + time.Minute))
	if _, err := r.Run(context.Background()); err != nil || h.calls != 1 || f.outboxRows(t) != 1 {
		t.Fatalf("second tick: err=%v calls=%d rows=%d, want no second run", err, h.calls, f.outboxRows(t))
	}
}

func TestTimedEventRunner_DoesNotFireTwiceAfterRestart(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	first := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("k")}
	if _, err := f.runner(t, true, 15*time.Minute, first).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	f.clock.Set(kickoff.Add(-time.Hour + 2*time.Minute))
	second := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("k")}
	if _, err := f.runner(t, true, 15*time.Minute, second).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if second.calls != 0 || f.outboxRows(t) != 1 {
		t.Fatalf("after restart: calls=%d rows=%d, want 0 and 1", second.calls, f.outboxRows(t))
	}
}

func TestTimedEventRunner_FiresInsideGraceWindow(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour + 14*time.Minute))
	h := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("k")}
	res, err := f.runner(t, true, 15*time.Minute, h).Run(context.Background())
	if err != nil || h.calls != 1 || res.Enqueued != 1 || res.Missed != 0 || f.alerts.count() != 0 {
		t.Fatalf("inside grace: err=%v calls=%d res=%+v alerts=%d", err, h.calls, res, f.alerts.count())
	}
}

func TestTimedEventRunner_SkipsAndAlertsOutsideGraceWindowOnce(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour + 16*time.Minute))
	h := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("k")}
	r := f.runner(t, true, 15*time.Minute, h)

	res, err := r.Run(context.Background())
	if err != nil || h.calls != 0 || res.Missed != 1 || f.outboxRows(t) != 0 {
		t.Fatalf("outside grace: err=%v calls=%d res=%+v rows=%d", err, h.calls, res, f.outboxRows(t))
	}
	if f.alerts.count() != 1 || !strings.Contains(f.alerts.messages[0], "was scheduled for") {
		t.Fatalf("alerts = %v, want one missed-window alert", f.alerts.messages)
	}
	if state, found := f.claimState(t, "weather", "k"); !found || state != "failed" {
		t.Fatalf("claim = %q found=%v, want failed", state, found)
	}

	f.clock.Set(f.clock.Now().Add(time.Minute))
	if res, _ := r.Run(context.Background()); res.Missed != 0 || f.alerts.count() != 1 {
		t.Fatalf("second tick: res=%+v alerts=%d, want the miss reported once", res, f.alerts.count())
	}
}

func TestTimedEventRunner_PanickingHandlerIsIsolated(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	bad := &fakeTimedHandler{name: "bad", events: dueAtKickoffMinusHour("k"), handle: func(context.Context, out.DueEvent) (out.EventResult, error) { panic("boom") }}
	badDue := &fakeTimedHandler{name: "baddue", duePan: true}
	good := &fakeTimedHandler{name: "good", events: dueAtKickoffMinusHour("k")}
	res, err := f.runner(t, true, 15*time.Minute, bad, badDue, good).Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Panics != 2 || good.calls != 1 || res.Enqueued != 1 || f.alerts.count() != 2 {
		t.Fatalf("res=%+v good.calls=%d alerts=%d, want 2 panics, good handler run, 2 alerts", res, good.calls, f.alerts.count())
	}
	if _, found := f.claimState(t, "bad", "k"); found {
		t.Fatal("a panicking handler must not leave a claim, so it can retry inside the grace window")
	}
}

func TestTimedEventRunner_ErrorAndTimeoutAreCountedAlertedAndRetried(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	failing := true
	h := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("k"), handle: func(ctx context.Context, ev out.DueEvent) (out.EventResult, error) {
		if failing {
			return out.EventResult{}, context.Canceled
		}
		return out.EventResult{Actions: []out.EventAction{out.EnqueuePublication{
			Destination: publishing.DestinationBluesky, Payload: publishing.MessagePayload{Text: "ok", Kind: "weather"},
		}}}, nil
	}}
	r := f.runner(t, true, 15*time.Minute, h)
	if res, _ := r.Run(context.Background()); res.Errors != 1 || f.alerts.count() != 1 {
		t.Fatalf("error tick: res=%+v alerts=%d", res, f.alerts.count())
	}

	h.handle = func(ctx context.Context, ev out.DueEvent) (out.EventResult, error) {
		<-ctx.Done()
		return out.EventResult{}, ctx.Err()
	}
	if res, _ := r.Run(context.Background()); res.Timeouts != 1 || f.alerts.count() != 2 {
		t.Fatalf("timeout tick: res=%+v alerts=%d", res, f.alerts.count())
	}

	failing = false
	h.handle = nil
	res, _ := r.Run(context.Background())
	if res.Enqueued != 1 || f.outboxRows(t) != 1 {
		t.Fatalf("recovered tick: res=%+v rows=%d, want a retry to publish", res, f.outboxRows(t))
	}
}

func TestTimedEventRunner_DisabledHandlerIsNotCalled(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	h := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("k")}
	r := f.runner(t, false, 15*time.Minute, h)
	if r.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", r.Len())
	}
	if _, err := r.Run(context.Background()); err != nil || h.calls != 0 || f.outboxRows(t) != 0 {
		t.Fatalf("disabled: err=%v calls=%d rows=%d", err, h.calls, f.outboxRows(t))
	}
}

func TestTimedEventRunner_InvalidPayloadRollsBackTheClaim(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	h := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("k"), handle: func(context.Context, out.DueEvent) (out.EventResult, error) {
		return out.EventResult{Actions: []out.EventAction{out.EnqueuePublication{Destination: publishing.DestinationBluesky, Payload: publishing.MessagePayload{Link: "https://example.com"}}}}, nil
	}}
	res, _ := f.runner(t, true, 15*time.Minute, h).Run(context.Background())
	if res.Errors != 1 || f.outboxRows(t) != 0 {
		t.Fatalf("res=%+v rows=%d", res, f.outboxRows(t))
	}
	if _, found := f.claimState(t, "weather", "k"); found {
		t.Fatal("an invalid payload must not take the claim")
	}
}

func TestTimedEventRunner_RejectsBadRegistration(t *testing.T) {
	f := newTimedFixture(t)
	deps := usecase.EventDeps{Clock: f.clock, Logger: fakeLogger{}, Claims: sqlite.NewEventClaimRepository(f.db)}
	build := func(handlers []out.TimedEventHandler, articleNames []string, deps usecase.EventDeps) error {
		_, err := usecase.NewTimedEventRunner(handlers, articleNames, nil, deps, nil, nil, nil, 0, 0)
		return err
	}
	if err := build([]out.TimedEventHandler{&fakeTimedHandler{name: " "}}, nil, deps); err == nil {
		t.Error("blank name accepted")
	}
	if err := build([]out.TimedEventHandler{&fakeTimedHandler{name: "a"}, &fakeTimedHandler{name: "a"}}, nil, deps); err == nil {
		t.Error("duplicate name accepted")
	}
	if err := build([]out.TimedEventHandler{&fakeTimedHandler{name: "a"}}, []string{"a"}, deps); err == nil {
		t.Error("name shared with an article handler accepted")
	}
	if err := build([]out.TimedEventHandler{nil}, nil, deps); err == nil {
		t.Error("nil handler accepted")
	}
	if err := build([]out.TimedEventHandler{&fakeTimedHandler{name: "a"}}, nil, usecase.EventDeps{Clock: f.clock, Logger: fakeLogger{}}); err == nil {
		t.Error("missing claims store accepted")
	}
}

func TestTimedEventRunner_PlaceholderArticleLoadsAndClaimReconciles(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	h := &fakeTimedHandler{name: "weather", events: dueAtKickoffMinusHour("2026-reg-1")}
	if _, err := f.runner(t, true, 15*time.Minute, h).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var articleID string
	if err := f.db.QueryRow(`SELECT article_id FROM event_claims WHERE handler = 'weather'`).Scan(&articleID); err != nil {
		t.Fatalf("claim article id: %v", err)
	}
	// PublishOutbox loads the article by id before every send.
	a, err := sqlite.NewArticleRepository(f.db).FindByID(context.Background(), articleID)
	if err != nil {
		t.Fatalf("FindByID(%q) error = %v", articleID, err)
	}
	if a.ID != "timed:weather:2026-reg-1" {
		t.Fatalf("article id = %q", a.ID)
	}
	if _, err := f.db.Exec(`UPDATE publication_outbox SET status = 'delivered' WHERE article_id = ?`, articleID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	published, failed, err := sqlite.NewEventClaimRepository(f.db).ReconcileWithOutbox(context.Background(), f.clock.Now())
	if err != nil || published != 1 || failed != 0 {
		t.Fatalf("Reconcile = %d, %d, %v, want the claim published", published, failed, err)
	}
}

// errorLogger records the messages and fields of Error calls.
type errorLogger struct {
	fakeLogger
	mu     sync.Mutex
	errors [][]any
}

func (l *errorLogger) Error(msg string, _ error, kv ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, append([]any{msg}, kv...))
}

func (f *timedFixture) runnerWithLogger(t *testing.T, logger out.Logger, grace time.Duration, h out.TimedEventHandler) *usecase.TimedEventRunner {
	t.Helper()
	r, err := usecase.NewTimedEventRunner([]out.TimedEventHandler{h}, nil,
		map[string]usecase.EventHandlerSettings{h.Name(): {Enabled: true}},
		usecase.EventDeps{Clock: f.clock, Logger: logger, Claims: sqlite.NewEventClaimRepository(f.db), Alerts: f.alerts},
		sqlite.NewArticleRepository(f.db), sqlite.NewOutboxRepository(f.db), sqlite.NewUnitOfWork(f.db), 200*time.Millisecond, grace)
	if err != nil {
		t.Fatalf("NewTimedEventRunner() error = %v", err)
	}
	return r
}

func TestTimedEventRunner_UntilKeepsEventRunnable(t *testing.T) {
	f := newTimedFixture(t)
	now := f.clock.Now()
	h := &fakeTimedHandler{name: "handler-a", events: []out.DueEvent{{Key: "2026-reg-1", At: now.Add(-2 * time.Hour), Until: now.Add(time.Hour)}}}
	res, err := f.runner(t, true, 15*time.Minute, h).Run(context.Background())
	if err != nil || h.calls != 1 || res.Missed != 0 {
		t.Fatalf("err=%v calls=%d res=%+v, want one call and no miss", err, h.calls, res)
	}
	if state, _ := f.claimState(t, "handler-a", "2026-reg-1"); state == "failed" {
		t.Fatalf("claim failed, want it runnable")
	}
}

func TestTimedEventRunner_PastUntilIsMissed(t *testing.T) {
	f := newTimedFixture(t)
	now := f.clock.Now()
	until := now.Add(-time.Minute)
	h := &fakeTimedHandler{name: "handler-a", events: []out.DueEvent{{Key: "2026-reg-1", At: now.Add(-2 * time.Hour), Until: until}}}
	res, err := f.runner(t, true, 15*time.Minute, h).Run(context.Background())
	if err != nil || h.calls != 0 || res.Missed != 1 {
		t.Fatalf("err=%v calls=%d res=%+v, want a miss and no call", err, h.calls, res)
	}
	if state, found := f.claimState(t, "handler-a", "2026-reg-1"); !found || state != "failed" {
		t.Fatalf("claim = %q found=%v, want failed", state, found)
	}
	var data string
	if err := f.db.QueryRow(`SELECT data FROM event_claims WHERE handler = ? AND claim_key = ?`, "handler-a", "2026-reg-1").Scan(&data); err != nil || !strings.Contains(data, `"reason":"missed_window"`) {
		t.Fatalf("claim data = %q err=%v, want reason missed_window", data, err)
	}
	if f.alerts.count() != 1 || !strings.Contains(f.alerts.messages[0], until.UTC().Format(time.RFC3339)) {
		t.Fatalf("alerts = %v, want one alert naming the deadline", f.alerts.messages)
	}
}

func TestTimedEventRunner_UntilBeforeAtUsesGrace(t *testing.T) {
	f := newTimedFixture(t)
	now := f.clock.Now()
	h := &fakeTimedHandler{name: "handler-a", events: []out.DueEvent{{Key: "2026-reg-1", At: now.Add(-5 * time.Minute), Until: now.Add(-10 * time.Minute)}}}
	logger := &errorLogger{}
	if _, err := f.runnerWithLogger(t, logger, 15*time.Minute, h).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1 (default grace)", h.calls)
	}
	logged := false
	for _, e := range logger.errors {
		s := ""
		for _, v := range e {
			s += " " + toString(v)
		}
		if strings.Contains(s, "handler") && strings.Contains(s, "key") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("errors = %v, want one error log with handler and key", logger.errors)
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func closeResult(reason string, extra ...out.EventAction) out.EventResult {
	return out.EventResult{Actions: append([]out.EventAction{out.CloseEvent{Reason: reason}}, extra...)}
}

func TestTimedEventRunner_CloseEventClaimsWithoutOutbox(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	h := &fakeTimedHandler{name: "handler-a", events: dueAtKickoffMinusHour("2026-reg-1"),
		handle: func(context.Context, out.DueEvent) (out.EventResult, error) { return closeResult("no overtime"), nil }}
	r := f.runner(t, true, 15*time.Minute, h)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if state, found := f.claimState(t, "handler-a", "2026-reg-1"); !found || state != "claimed" {
		t.Fatalf("claim = %q found=%v, want claimed", state, found)
	}
	var articleID, data string
	if err := f.db.QueryRow(`SELECT article_id, data FROM event_claims WHERE handler = ? AND claim_key = ?`, "handler-a", "2026-reg-1").Scan(&articleID, &data); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	var got struct{ Reason, Detail string }
	if err := json.Unmarshal([]byte(data), &got); err != nil || articleID != "" || got.Reason != usecase.TimedEventClosedReason || got.Detail != "no overtime" {
		t.Fatalf("article_id=%q data=%s err=%v", articleID, data, err)
	}
	if f.outboxRows(t) != 0 || f.alerts.count() != 0 {
		t.Fatalf("rows=%d alerts=%d, want none", f.outboxRows(t), f.alerts.count())
	}
	f.clock.Set(f.clock.Now().Add(time.Minute))
	if _, err := r.Run(context.Background()); err != nil || h.calls != 1 {
		t.Fatalf("second tick: err=%v calls=%d, want 1", err, h.calls)
	}
}

func TestTimedEventRunner_CloseEventWithPublicationIsError(t *testing.T) {
	f := newTimedFixture(t)
	f.clock.Set(kickoff.Add(-time.Hour))
	h := &fakeTimedHandler{name: "handler-a", events: dueAtKickoffMinusHour("2026-reg-1"),
		handle: func(context.Context, out.DueEvent) (out.EventResult, error) {
			return closeResult("x", out.EnqueuePublication{
				Destination: publishing.DestinationBluesky,
				Payload:     publishing.MessagePayload{Text: "hello", Kind: "weather"},
			}), nil
		}}
	r := f.runner(t, true, 15*time.Minute, h)
	res, err := r.Run(context.Background())
	if err != nil || res.Errors != 1 {
		t.Fatalf("err=%v res=%+v, want one error", err, res)
	}
	if _, found := f.claimState(t, "handler-a", "2026-reg-1"); found || f.outboxRows(t) != 0 {
		t.Fatalf("claim found=%v rows=%d, want neither", found, f.outboxRows(t))
	}
	f.clock.Set(f.clock.Now().Add(time.Minute))
	if _, err := r.Run(context.Background()); err != nil || h.calls != 2 {
		t.Fatalf("second tick: err=%v calls=%d, want 2", err, h.calls)
	}
}
