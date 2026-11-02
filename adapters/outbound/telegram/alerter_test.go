package telegram_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/telegram"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeTelegram struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTelegram) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

func (f *fakeTelegram) setStatus(s int) {
	f.mu.Lock()
	f.status = s
	f.mu.Unlock()
}

func newTestAlerter(f *fakeTelegram, now *time.Time, secrets ...string) *telegram.Alerter {
	return telegram.NewAlerter(telegram.AlerterConfig{
		ChatID: "-100", BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij", BaseURL: f.URL,
		Timeout: 2 * time.Second, Secrets: secrets, Now: func() time.Time { return *now },
	})
}

func TestAlerter_SendsPlainTextMarkedAsAlert(t *testing.T) {
	f := newFakeTelegram(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	al := newTestAlerter(f, &now)

	err := al.Alert(context.Background(), appout.Alert{
		Source: "outbox", Severity: appout.AlertCritical, Title: "Publication to bluesky is dead", Details: "last error: <b>boom</b> & more",
	})
	if err != nil {
		t.Fatalf("Alert() error = %v", err)
	}
	got := f.sent()
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1", len(got))
	}
	if got[0]["chat_id"] != "-100" {
		t.Errorf("chat_id = %v", got[0]["chat_id"])
	}
	if _, ok := got[0]["parse_mode"]; ok {
		t.Errorf("parse_mode = %v, want none (plain text, nothing to escape)", got[0]["parse_mode"])
	}
	text, _ := got[0]["text"].(string)
	if !strings.HasPrefix(text, "[alert] critical · outbox\nPublication to bluesky is dead") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "<b>boom</b> & more") {
		t.Errorf("details missing or altered: %q", text)
	}
}

func TestAlerter_DedupWindow(t *testing.T) {
	f := newFakeTelegram(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	al := newTestAlerter(f, &now)
	ctx := context.Background()
	a := appout.Alert{Source: "weekly", Title: "t", DedupKey: "weekly:ocr"}

	for i := 0; i < 3; i++ {
		if err := al.Alert(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.sent()); n != 1 {
		t.Fatalf("sent %d within the window, want 1", n)
	}
	other := a
	other.DedupKey = "weekly:other"
	if err := al.Alert(ctx, other); err != nil {
		t.Fatal(err)
	}
	if n := len(f.sent()); n != 2 {
		t.Fatalf("a different key must go through; sent = %d", n)
	}

	now = now.Add(23 * time.Hour)
	_ = al.Alert(ctx, a)
	if n := len(f.sent()); n != 2 {
		t.Fatalf("still inside the 24h window; sent = %d", n)
	}
	now = now.Add(2 * time.Hour)
	_ = al.Alert(ctx, a)
	if n := len(f.sent()); n != 3 {
		t.Fatalf("window elapsed; sent = %d, want 3", n)
	}
}

func TestAlerter_EmptyDedupKeyIsNeverSuppressed(t *testing.T) {
	f := newFakeTelegram(t)
	now := time.Now()
	al := newTestAlerter(f, &now)
	for i := 0; i < 2; i++ {
		if err := al.Alert(context.Background(), appout.Alert{Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.sent()); n != 2 {
		t.Fatalf("sent = %d, want 2", n)
	}
}

func TestAlerter_FailedDeliveryReturnsErrorAndDoesNotConsumeTheWindow(t *testing.T) {
	f := newFakeTelegram(t)
	now := time.Now()
	al := newTestAlerter(f, &now)
	a := appout.Alert{Title: "t", DedupKey: "k"}

	f.setStatus(http.StatusInternalServerError)
	if err := al.Alert(context.Background(), a); err == nil {
		t.Fatal("Alert() error = nil, want the delivery failure")
	}
	f.setStatus(http.StatusOK)
	if err := al.Alert(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if n := len(f.sent()); n != 2 {
		t.Fatalf("requests = %d, want the retry to go out", n)
	}
}

func TestAlerter_TruncatesLongText(t *testing.T) {
	f := newFakeTelegram(t)
	now := time.Now()
	al := newTestAlerter(f, &now)
	long := strings.Repeat("é", 10000)
	if err := al.Alert(context.Background(), appout.Alert{Title: long, Details: long}); err != nil {
		t.Fatal(err)
	}
	text, _ := f.sent()[0]["text"].(string)
	if n := utf8.RuneCountInString(text); n > 2000 {
		t.Errorf("text has %d runes, want it bounded", n)
	}
	if !strings.Contains(text, "…") || !utf8.ValidString(text) {
		t.Errorf("text not cleanly truncated: valid=%v", utf8.ValidString(text))
	}
}

func TestAlerter_NeverLeaksSecrets(t *testing.T) {
	f := newFakeTelegram(t)
	now := time.Now()
	al := newTestAlerter(f, &now, "hunter2-secret")
	err := al.Alert(context.Background(), appout.Alert{
		Title: "Post https://api.telegram.org/bot123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij/sendMessage failed",
		Details: "token 123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij password abcd-efgh-ijkl-mnop " +
			"explicit hunter2-secret url https://x.example/p?access_token=abc123 Bearer abc.def",
	})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := f.sent()[0]["text"].(string)
	for _, leak := range []string{"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij", "abcd-efgh-ijkl-mnop", "hunter2-secret", "abc123", "abc.def"} {
		if strings.Contains(text, leak) {
			t.Errorf("alert text leaks %q: %q", leak, text)
		}
	}
}
