package telegramupdates_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/inbound/telegramupdates"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/telegram"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)        {}
func (nopLogger) Info(string, ...any)         {}
func (nopLogger) Warn(string, ...any)         {}
func (nopLogger) Error(string, error, ...any) {}

type fakeAPI struct {
	mu       sync.Mutex
	offsets  []int64
	answers  []string // callback_query_id|text
	getCalls int
	// handler for getUpdates: returns status and body for the n-th call (0-based).
	updates func(n int) (int, string)
	srv     *httptest.Server
}

func newFakeAPI(t *testing.T, updates func(n int) (int, string)) *fakeAPI {
	f := &fakeAPI{updates: updates}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			f.offsets = append(f.offsets, int64(body["offset"].(float64)))
			code, resp := f.updates(f.getCalls)
			f.getCalls++
			w.WriteHeader(code)
			_, _ = w.Write([]byte(resp))
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			txt, _ := body["text"].(string)
			f.answers = append(f.answers, fmt.Sprintf("%v|%s", body["callback_query_id"], txt))
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type fakeOffsets struct {
	mu     sync.Mutex
	stored []int64
	start  int64
}

func (o *fakeOffsets) Load(context.Context, string) (int64, error) { return o.start, nil }
func (o *fakeOffsets) Store(_ context.Context, _ string, n int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stored = append(o.stored, n)
	return nil
}

type fakeTasks struct {
	mu      sync.Mutex
	signals []string
	err     error
}

func (f *fakeTasks) AddSignal(_ context.Context, id int64, payload json.RawMessage, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, fmt.Sprintf("%d:%s", id, payload))
	return f.err
}

type fakeRouter struct {
	mu     sync.Mutex
	routed []out.TelegramUpdate
}

func (r *fakeRouter) Route(u out.TelegramUpdate) (int64, json.RawMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routed = append(r.routed, u)
	return 5, json.RawMessage(`{"d":"` + u.CallbackData + `"}`), true
}

const callbackUpdate = `{"update_id":%d,"callback_query":{"id":"cb%d","from":{"id":%d},"data":"ok","message":{"message_id":4,"chat":{"id":22}}}}`

func cb(id, user int) string { return fmt.Sprintf(callbackUpdate, id, id, user) }

// run starts the poller and cancels it once stop() reports true.
func run(t *testing.T, f *fakeAPI, offs *fakeOffsets, tasks *fakeTasks, router *fakeRouter, sleep func(context.Context, time.Duration), stop func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := telegramupdates.New(telegramupdates.Config{
		Bot: telegram.NewBotClient(f.srv.URL, token, 2*time.Second), Offsets: offs, Tasks: tasks, Router: router,
		AllowedUserID: 11, AllowedChatID: 22, BotKey: "123456", Logger: nopLogger{}, Sleep: sleep,
	})
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !stop() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func empty(int) (int, string) { return 200, `{"ok":true,"result":[]}` }

func TestPollerRoutesAuthorizedCallback(t *testing.T) {
	f := newFakeAPI(t, func(n int) (int, string) {
		if n == 0 {
			return 200, `{"ok":true,"result":[` + cb(7, 11) + `]}`
		}
		return empty(n)
	})
	tasks, router := &fakeTasks{}, &fakeRouter{}
	run(t, f, &fakeOffsets{}, tasks, router, func(context.Context, time.Duration) {}, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.answers) == 1
	})
	if len(tasks.signals) != 1 || tasks.signals[0] != `5:{"d":"ok"}` {
		t.Errorf("signals = %v", tasks.signals)
	}
	if len(f.answers) != 1 || f.answers[0] != "cb7|" {
		t.Errorf("answers = %v", f.answers)
	}
}

func TestPollerDropsForeignUser(t *testing.T) {
	f := newFakeAPI(t, func(n int) (int, string) {
		if n == 0 {
			return 200, `{"ok":true,"result":[` + cb(7, 99) + `,{"update_id":8,"message":{"message_id":1,"from":{"id":11},"chat":{"id":77},"text":"x"}}]}`
		}
		return empty(n)
	})
	tasks, router := &fakeTasks{}, &fakeRouter{}
	offs := &fakeOffsets{}
	run(t, f, offs, tasks, router, func(context.Context, time.Duration) {}, func() bool {
		offs.mu.Lock()
		defer offs.mu.Unlock()
		return len(offs.stored) == 1
	})
	if len(router.routed) != 0 || len(tasks.signals) != 0 || len(f.answers) != 0 {
		t.Errorf("routed=%v signals=%v answers=%v, want none", router.routed, tasks.signals, f.answers)
	}
	if offs.stored[0] != 9 {
		t.Errorf("offset stored = %d, want 9 (dropped updates still advance)", offs.stored[0])
	}
}

func TestPollerStoresOffsetAfterBatch(t *testing.T) {
	f := newFakeAPI(t, func(n int) (int, string) {
		if n == 0 {
			return 200, `{"ok":true,"result":[` + cb(7, 11) + `,` + cb(9, 11) + `]}`
		}
		return empty(n)
	})
	offs := &fakeOffsets{start: 3}
	run(t, f, offs, &fakeTasks{}, &fakeRouter{}, func(context.Context, time.Duration) {}, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.getCalls >= 2
	})
	if len(offs.stored) == 0 || offs.stored[0] != 10 {
		t.Errorf("stored = %v, want first 10", offs.stored)
	}
	if f.offsets[0] != 3 || f.offsets[1] != 10 {
		t.Errorf("getUpdates offsets = %v, want [3 10 ...]", f.offsets)
	}
}

func TestPollerAnswersClosedTask(t *testing.T) {
	f := newFakeAPI(t, func(n int) (int, string) {
		if n == 0 {
			return 200, `{"ok":true,"result":[` + cb(7, 11) + `]}`
		}
		return empty(n)
	})
	tasks := &fakeTasks{err: out.ErrTaskClosed}
	run(t, f, &fakeOffsets{}, tasks, &fakeRouter{}, func(context.Context, time.Duration) {}, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.answers) >= 1
	})
	if len(f.answers) != 1 || f.answers[0] != "cb7|Este borrador ya está cerrado" {
		t.Errorf("answers = %v", f.answers)
	}
}

func TestPollerAnswersCallbackWhenAddSignalFails(t *testing.T) {
	f := newFakeAPI(t, func(n int) (int, string) {
		if n == 0 {
			return 200, `{"ok":true,"result":[` + cb(7, 11) + `]}`
		}
		return empty(n)
	})
	tasks := &fakeTasks{err: errors.New("db down")}
	run(t, f, &fakeOffsets{}, tasks, &fakeRouter{}, func(context.Context, time.Duration) {}, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.answers) >= 1
	})
	if len(f.answers) != 1 || f.answers[0] != "cb7|" {
		t.Errorf("answers = %v", f.answers)
	}
}

func TestPollerBacksOffOn409(t *testing.T) {
	f := newFakeAPI(t, func(n int) (int, string) {
		if n < 3 {
			return 409, `{"ok":false,"error_code":409,"description":"Conflict"}`
		}
		return empty(n)
	})
	var mu sync.Mutex
	var sleeps []time.Duration
	run(t, f, &fakeOffsets{}, &fakeTasks{}, &fakeRouter{}, func(_ context.Context, d time.Duration) {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
	}, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.getCalls >= 4
	})
	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) < 3 || sleeps[0] != 5*time.Second || sleeps[1] != 10*time.Second || sleeps[2] != 20*time.Second {
		t.Errorf("sleeps = %v, want 5s,10s,20s", sleeps)
	}
}

func TestBackoffCapsAtTwoMinutes(t *testing.T) {
	if d := telegramupdates.Backoff(10); d != 2*time.Minute {
		t.Errorf("Backoff(10) = %v", d)
	}
	if d := telegramupdates.Backoff(0); d != 5*time.Second {
		t.Errorf("Backoff(0) = %v", d)
	}
}
