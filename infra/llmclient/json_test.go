// infra/llmclient/json_test.go
package llmclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
)

func TestCompleteJSONReturnsUnfencedJSON(t *testing.T) {
	srv := newScriptedServer(t, scriptedCompletion{content: "```json\n{\"a\":1}\n```", finishReason: "stop"})
	c := llmclient.New(llmclient.Config{BaseURL: srv.URL, Model: "m", Timeout: 5 * time.Second})
	got, err := c.CompleteJSON(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if got != `{"a":1}` {
		t.Fatalf("got %q, want %q", got, `{"a":1}`)
	}
}

func TestCompleteJSONRetriesOnceOnInvalidJSON(t *testing.T) {
	srv := newScriptedServer(
		t,
		scriptedCompletion{content: "no json", finishReason: "stop"},
		scriptedCompletion{content: `{"ok":true}`, finishReason: "stop"},
	)
	c := llmclient.New(llmclient.Config{BaseURL: srv.URL, Model: "m", Timeout: 5 * time.Second})
	got, err := c.CompleteJSON(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("got %q", got)
	}
	if srv.requests != 2 {
		t.Fatalf("requests = %d, want 2", srv.requests)
	}
}

func TestCompleteJSONNoRateLimitRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	restore := llmclient.SetSleepForTest(func(context.Context, time.Duration) error { return nil })
	defer restore()

	c := llmclient.New(llmclient.Config{BaseURL: srv.URL, Model: "m", Timeout: 5 * time.Second, NoRateLimitRetry: true})
	_, err := c.CompleteJSON(context.Background(), "sys", "user")
	if err == nil {
		t.Fatal("expected error")
	}
	if hits.Load() != 1 {
		t.Fatalf("requests = %d, want 1", hits.Load())
	}
	if code, ok := llmclient.StatusCode(err); !ok || code != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = (%d, %v), want (429, true)", code, ok)
	}
}

func TestStatusCodeOnNonHTTPError(t *testing.T) {
	if code, ok := llmclient.StatusCode(errors.New("x")); ok || code != 0 {
		t.Fatalf("StatusCode = (%d, %v), want (0, false)", code, ok)
	}
}
