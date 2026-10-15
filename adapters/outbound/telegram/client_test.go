package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestSendMessage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/bot123:abc/sendMessage") {
			t.Errorf("path = %q, want suffix /bot123:abc/sendMessage", r.URL.Path)
		}
		if _, err := w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	messageID, err := sendMessage(context.Background(), client, server.URL, "123:abc", map[string]any{"chat_id": "-100", "text": "hi"})
	if err != nil {
		t.Fatalf("sendMessage() error = %v", err)
	}
	if messageID != "42" {
		t.Errorf("messageID = %q, want %q", messageID, "42")
	}
}

func TestSendMessage_429ReturnsRateLimitedErrorWithRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		if _, err := w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 30","parameters":{"retry_after":30}}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "123:abc", map[string]any{})

	var rateLimited *appout.RateLimitedError
	if !errors.As(err, &rateLimited) {
		t.Fatalf("err = %v, want *out.RateLimitedError", err)
	}
	if rateLimited.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", rateLimited.RetryAfter)
	}
}

func TestSendMessage_429WithoutRetryAfterDefaultsTo30Seconds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		if _, err := w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests"}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "123:abc", map[string]any{})

	var rateLimited *appout.RateLimitedError
	if !errors.As(err, &rateLimited) {
		t.Fatalf("err = %v, want *out.RateLimitedError", err)
	}
	if rateLimited.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want default 30s", rateLimited.RetryAfter)
	}
}

func TestSendMessage_400IsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "123:abc", map[string]any{})

	var permanent *appout.PermanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("err = %v, want *out.PermanentError", err)
	}
}

func TestSendMessage_403IsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the channel"}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "123:abc", map[string]any{})

	var permanent *appout.PermanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("err = %v, want *out.PermanentError", err)
	}
}

func TestSendMessage_500IsTemporary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal Server Error"}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "123:abc", map[string]any{})
	if err == nil {
		t.Fatal("err = nil, want a plain temporary error")
	}
	var permanent *appout.PermanentError
	var rateLimited *appout.RateLimitedError
	if errors.As(err, &permanent) || errors.As(err, &rateLimited) {
		t.Errorf("err = %v, want a plain (temporary) error, not permanent/rate-limited", err)
	}
}

func TestSendMessage_TransportErrorNeverLeaksToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // closed before use: client.Do will fail with connection refused

	const token = "123456:SUPER-SECRET-TOKEN"
	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, token, map[string]any{})
	if err == nil {
		t.Fatal("err = nil, want a transport error against a closed server")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("err = %q, want the bot token redacted from the error string", err.Error())
	}
}

func TestSendMessage_MalformedTokenNeverLeaksIntoRequestBuildError(t *testing.T) {
	const token = "123456:SECRET%ZZinvalid"
	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, "https://api.telegram.org", token, map[string]any{})
	if err == nil {
		t.Fatal("err = nil, want a request-build error for a malformed token")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %q, want the bot token redacted from the error string", err.Error())
	}
}

func TestSendMessage_ResponseExceedingLimitErrors(t *testing.T) {
	huge := strings.Repeat("x", responseLimitBytes+1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`{"ok":true,"result":{"message_id":1,"padding":"` + huge + `"}}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "123:abc", map[string]any{})
	if err == nil {
		t.Fatal("err = nil, want an error for an oversized response")
	}
}

func TestSendMessage_TransportErrorIsAmbiguous(t *testing.T) {
	client := &http.Client{Timeout: 50 * time.Millisecond}
	// No listener on this port: Do() fails before any byte reaches a
	// server — still classified ambiguous by design (see client.go's
	// classification comment: distinguishing "never sent" from "sent,
	// no response" is not reliable across platforms, so every client.Do
	// failure is conservatively ambiguous).
	_, err := sendMessage(context.Background(), client, "http://127.0.0.1:1", "token", map[string]any{"chat_id": "x"})

	var ambiguous *appout.AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err = %v (%T), want *out.AmbiguousError", err, err)
	}
}

func TestSendMessage_ResponseTimeoutIsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond) // longer than the client's timeout below
	}))
	defer server.Close()

	client := &http.Client{Timeout: 20 * time.Millisecond}
	_, err := sendMessage(context.Background(), client, server.URL, "token", map[string]any{"chat_id": "x"})

	var ambiguous *appout.AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err = %v (%T), want *out.AmbiguousError", err, err)
	}
}

func TestSendMessage_TruncatedBodyIsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000") // promise more than we write
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true`)) // then close without finishing
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "token", map[string]any{"chat_id": "x"})

	var ambiguous *appout.AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err = %v (%T), want *out.AmbiguousError", err, err)
	}
}

func TestSendMessage_MalformedJSONOnOKStatusIsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "token", map[string]any{"chat_id": "x"})

	var ambiguous *appout.AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err = %v (%T), want *out.AmbiguousError", err, err)
	}
}

func TestSendMessage_ConfirmedRejectionIsNotAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	_, err := sendMessage(context.Background(), client, server.URL, "token", map[string]any{"chat_id": "x"})

	var ambiguous *appout.AmbiguousError
	if errors.As(err, &ambiguous) {
		t.Fatalf("err = %v, want a confirmed *out.PermanentError, not ambiguous — Telegram gave us a real answer", err)
	}
	var permanent *appout.PermanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("err = %v (%T), want *out.PermanentError", err, err)
	}
}
