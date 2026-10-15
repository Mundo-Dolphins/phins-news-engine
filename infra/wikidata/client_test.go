package wikidata

import (
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const testUA = "news-engine-test/1.0 (https://example.com/contact)"

type sleepRecorder struct {
	mu    sync.Mutex
	calls []time.Duration
}

func (s *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, d)
	return nil
}

func newTestClient(t *testing.T, h http.HandlerFunc, mutate func(*Config)) (*Client, *sleepRecorder) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// AllowPrivateHosts: httptest listens on loopback, refused by default.
	cfg := Config{BaseURL: srv.URL, Language: "en", UserAgent: testUA, Timeout: 2 * time.Second, MaxCandidates: 5, AllowPrivateHosts: true}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	rec := &sleepRecorder{}
	c.sleep = rec.sleep
	return c, rec
}

const searchBody = `{"search":[
 {"id":"Q1","label":"Patrick Ostrander","description":"American football quarterback","match":{"type":"label","text":"Patrick Ostrander"}},
 {"id":"Q2","label":"Patrick Ostrander Sr.","description":"baseball pitcher","aliases":["Pat Ostrander"]},
 {"id":"Q3","label":"Ostrander","description":"surname"},
 {"id":"Q4","label":"Four"},{"id":"Q5","label":"Five"}],"success":1}`

func TestNew_ValidatesConfig(t *testing.T) {
	good := Config{BaseURL: "https://wikidata.example.org/w/api.php", Language: "en", UserAgent: testUA, Timeout: time.Second, MaxCandidates: 5}
	if _, err := New(good); err != nil {
		t.Fatalf("New(good) error = %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"no base url":    func(c *Config) { c.BaseURL = "" },
		"no language":    func(c *Config) { c.Language = "" },
		"no user agent":  func(c *Config) { c.UserAgent = "" },
		"no timeout":     func(c *Config) { c.Timeout = 0 },
		"no max cands":   func(c *Config) { c.MaxCandidates = 0 },
		"bad property":   func(c *Config) { c.Properties = []string{"54"} },
		"neg concurrent": func(c *Config) { c.MaxConcurrency = -1 },
	} {
		cfg := good
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%s) error = nil, want error", name)
		}
	}
}

func TestSearch_EncodesQueryAndParams(t *testing.T) {
	var got map[string]string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got = map[string]string{}
		for _, k := range []string{"action", "format", "search", "language", "uselang", "type", "limit"} {
			got[k] = q.Get(k)
		}
		_, _ = w.Write([]byte(searchBody))
	}, nil)

	if _, err := c.Search(context.Background(), "  Pat & Ostrander=? ", 3); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	want := map[string]string{"action": "wbsearchentities", "format": "json", "search": "Pat & Ostrander=?", "language": "en", "uselang": "en", "type": "item", "limit": "3"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("param %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestSearch_PreservesOrderAndBoundsResults(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(searchBody)) }, nil)

	got, err := c.Search(context.Background(), "ostrander", 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "Q1" || got[1].ID != "Q2" {
		t.Fatalf("Search() = %+v, want [Q1 Q2] in provider order", got)
	}
	if got[0].Description != "American football quarterback" || len(got[1].Aliases) != 1 || got[1].Aliases[0] != "Pat Ostrander" {
		t.Errorf("descriptions/aliases not normalized: %+v", got)
	}

	// limit above MaxCandidates is capped to MaxCandidates (5 in the fixture; use 3).
	c2, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(searchBody)) }, func(cfg *Config) { cfg.MaxCandidates = 3 })
	got, _ = c2.Search(context.Background(), "ostrander", 50)
	if len(got) != 3 {
		t.Fatalf("len(Search()) = %d, want 3 (MaxCandidates cap)", len(got))
	}
}

func TestSearch_EmptyQueryOrLimitMakesNoRequest(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, nil)
	for _, tc := range []struct {
		q string
		l int
	}{{"   ", 3}, {"x", 0}} {
		got, err := c.Search(context.Background(), tc.q, tc.l)
		if err != nil || got != nil {
			t.Fatalf("Search(%q,%d) = %v,%v want nil,nil", tc.q, tc.l, got, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("server called %d times, want 0", calls.Load())
	}
}

func TestDo_SendsUserAgentAndNegotiatesGzip(t *testing.T) {
	var ua, ae string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		ua, ae = r.Header.Get("User-Agent"), r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte(searchBody))
		_ = zw.Close()
	}, nil)
	got, err := c.Search(context.Background(), "ostrander", 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("Search() = %v, %v", got, err)
	}
	if ua != testUA {
		t.Errorf("User-Agent = %q, want %q", ua, testUA)
	}
	if !strings.Contains(ae, "gzip") {
		t.Errorf("Accept-Encoding = %q, want gzip", ae)
	}
}

func TestDo_Retries5xxOnceThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(searchBody))
	}, nil)
	if _, err := c.Search(context.Background(), "ostrander", 1); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if calls.Load() != 2 || len(rec.calls) != 1 {
		t.Fatalf("calls=%d sleeps=%v, want 2 calls and 1 backoff sleep", calls.Load(), rec.calls)
	}
}

func TestDo_5xxTwiceReturnsTransient(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, nil)
	_, err := c.Search(context.Background(), "ostrander", 1)
	if !errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want exactly 2 (one bounded retry)", calls.Load())
	}
}

func TestDo_429HonorsRetryAfterSeconds(t *testing.T) {
	var calls atomic.Int32
	c, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(searchBody))
	}, nil)
	if _, err := c.Search(context.Background(), "ostrander", 1); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != 7*time.Second {
		t.Fatalf("sleeps = %v, want [7s]", rec.calls)
	}
}

func TestDo_429HonorsRetryAfterHTTPDate(t *testing.T) {
	fixed := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	c, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", fixed.Add(9*time.Second).Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(searchBody))
	}, nil)
	c.now = func() time.Time { return fixed }
	if _, err := c.Search(context.Background(), "ostrander", 1); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != 9*time.Second {
		t.Fatalf("sleeps = %v, want [9s]", rec.calls)
	}
}

func TestDo_RetryAfterOverCapFailsTransientWithoutSleeping(t *testing.T) {
	var calls atomic.Int32
	c, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}, nil)
	_, err := c.Search(context.Background(), "ostrander", 1)
	if !errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
	if calls.Load() != 1 || len(rec.calls) != 0 {
		t.Fatalf("calls=%d sleeps=%v, want 1 call and no sleep", calls.Load(), rec.calls)
	}
}

func TestDo_4xxIsNotRetriedAndNotTransient(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}, nil)
	_, err := c.Search(context.Background(), "ostrander", 1)
	if err == nil || errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want non-transient error", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", calls.Load())
	}
}

func TestDo_MalformedJSONIsNotRetriedAndNotTransient(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{not json`))
	}, nil)
	_, err := c.Search(context.Background(), "ostrander", 1)
	if err == nil || errors.Is(err, out.ErrTransient) || calls.Load() != 1 {
		t.Fatalf("error = %v calls = %d, want non-transient error after 1 call", err, calls.Load())
	}
}

func TestDo_ResponseOverSizeLimitIsRejected(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"search":[],"pad":"` + strings.Repeat("x", 200) + `"}`))
	}, func(cfg *Config) { cfg.MaxResponseBytes = 100 })
	_, err := c.Search(context.Background(), "ostrander", 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds") || errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want non-transient size-limit error", err)
	}
}

func TestDo_TimeoutIsTransient(t *testing.T) {
	release := make(chan struct{})
	c, _ := newTestClient(t, func(http.ResponseWriter, *http.Request) { <-release }, func(cfg *Config) { cfg.Timeout = 50 * time.Millisecond })
	// Registered after newTestClient so it runs before srv.Close (LIFO).
	t.Cleanup(func() { close(release) })
	_, err := c.Search(context.Background(), "ostrander", 1)
	if !errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
}

func TestDo_ContextCancellationIsNotTransient(t *testing.T) {
	release := make(chan struct{})
	c, _ := newTestClient(t, func(http.ResponseWriter, *http.Request) { <-release }, nil)
	// Registered after newTestClient so it runs before srv.Close (LIFO).
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Search(ctx, "ostrander", 1)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, out.ErrTransient) {
		t.Fatalf("error = %v, want context.DeadlineExceeded and not transient", err)
	}
}

func TestDo_APIErrorPayload(t *testing.T) {
	for code, wantTransient := range map[string]bool{"maxlag": true, "ratelimited": true, "badvalue": false} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","info":"x"}}`))
		}, nil)
		_, err := c.Search(context.Background(), "ostrander", 1)
		if err == nil || errors.Is(err, out.ErrTransient) != wantTransient {
			t.Errorf("code %s: error = %v, transient want %v", code, err, wantTransient)
		}
	}
}

func TestDo_ConcurrencyIsCapped(t *testing.T) {
	var inFlight, maxSeen atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			m := maxSeen.Load()
			if n <= m || maxSeen.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = w.Write([]byte(searchBody))
	}, nil) // MaxConcurrency 0 => 1
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Search(context.Background(), "ostrander", 1)
		}()
	}
	wg.Wait()
	if maxSeen.Load() != 1 {
		t.Fatalf("max in-flight = %d, want 1", maxSeen.Load())
	}
}

func TestRequestsAreGETReadOnly(t *testing.T) {
	var method string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write([]byte(searchBody))
	}, nil)
	_, _ = c.Search(context.Background(), "ostrander", 1)
	if method != http.MethodGet {
		t.Fatalf("method = %s, want GET", method)
	}
}
