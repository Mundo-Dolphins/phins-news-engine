// infra/llmclient/transport_test.go
package llmclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestResolver_Resolve_RetriesOnce429ThenSucceeds(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(chatCompletionResponse(t, `{"resolved":true,"type":"player","canonical_name":"Random Newcomer","confidence":0.9}`))
	}))
	defer server.Close()

	var delays []time.Duration
	defer llmclient.SetSleepForTest(func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	})()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 10 * time.Second})
	result, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "title", Context: "x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want the 429 retry to succeed", err)
	}
	if !result.Resolved || result.CanonicalName != "Random Newcomer" {
		t.Fatalf("Resolve() = %+v, want resolved Random Newcomer", result)
	}
	if attempts != 2 {
		t.Fatalf("server got %d requests, want 2 (first 429, retry succeeds)", attempts)
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("requested delays = %v, want [1s] (Retry-After honored)", delays)
	}
}

func TestResolver_Resolve_Retries503Once(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(chatCompletionResponse(t, `{"resolved":false}`))
	}))
	defer server.Close()

	var delays []time.Duration
	defer llmclient.SetSleepForTest(func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	})()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 10 * time.Second})
	result, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "X", NormalizedText: "x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want the 503 retry to succeed", err)
	}
	if result.Resolved {
		t.Fatalf("Resolve() = %+v, want Resolved=false", result)
	}
	if attempts != 2 {
		t.Fatalf("server got %d requests, want 2 (first 503, retry succeeds)", attempts)
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("requested delays = %v, want [1s] (default wait without Retry-After)", delays)
	}
}

func TestBoundedRetryWait(t *testing.T) {
	for _, tc := range []struct{ in, want time.Duration }{
		{0, time.Second},
		{-time.Second, time.Second},
		{2 * time.Second, 2 * time.Second},
		{10 * time.Second, 3 * time.Second},
	} {
		if got := llmclient.BoundedRetryWait(tc.in); got != tc.want {
			t.Errorf("BoundedRetryWait(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestResolver_Resolve_400DoesNotTriggerRateLimitRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 10 * time.Second})
	_, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "X", NormalizedText: "x"})
	if err == nil {
		t.Fatal("Resolve() error = nil, want an error (both attempts return 400)")
	}
	// completeWithFallback's existing behavior: one attempt with
	// response_format, one without — neither is the new rate-limit
	// retry, since 400 is not retryable.
	if attempts != 2 {
		t.Fatalf("server got %d requests, want 2 (existing with/without response_format pair, no extra rate-limit retry)", attempts)
	}
}

func TestResolver_Resolve_RateLimitRetryAbandonedOnContextCancel(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "30") // far longer than the test's context timeout
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 10 * time.Second})
	start := time.Now()
	_, err := resolver.Resolve(ctx, appout.EntityResolutionCandidate{Text: "X", NormalizedText: "x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Resolve() error = nil, want an error once the context is cancelled")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed = %v, want well under the 3s retry cap since the context cancels first", elapsed)
	}
}

func TestResolver_Resolve_TimeoutDoesNotSubmitCompatibilityRetry(t *testing.T) {
	attempts := 0
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		close(started)
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 20 * time.Millisecond})
	_, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "X", NormalizedText: "x", Field: "title", Context: "x"})
	if err == nil {
		t.Fatal("Resolve() error = nil, want timeout")
	}
	<-started
	if attempts != 1 {
		t.Fatalf("server got %d requests, want 1: a timeout must not submit a compatibility retry", attempts)
	}
}
