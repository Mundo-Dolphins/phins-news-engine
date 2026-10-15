package reddit_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/reddit"
)

const secretToken = "SECRET-TOKEN-123"

type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLogger) record(msg string, kv ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg+" "+fmt.Sprint(kv...))
}
func (l *recordingLogger) Debug(string, ...any)       {}
func (l *recordingLogger) Info(string, ...any)        {}
func (l *recordingLogger) Warn(msg string, kv ...any) { l.record(msg, kv...) }
func (l *recordingLogger) Error(msg string, err error, kv ...any) {
	l.record(msg+" "+err.Error(), kv...)
}

func TestFetch_LoggedErrorsNeverContainToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("cannot hijack")
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer server.Close()
	logger := &recordingLogger{}
	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "examples"}}, testClientConfig(), logger)
	f.BaseURL = server.URL + "/?token=" + secretToken
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	logged := strings.Join(logger.lines, "\n")
	if logged == "" || strings.Contains(logged, secretToken) {
		t.Fatalf("log must record the failure without the token:\n%s", logged)
	}
}

func TestFetch_DefaultPolicyBlocksPrivateBaseURL(t *testing.T) {
	cfg := testClientConfig()
	cfg.AllowPrivateHosts = false
	logger := &recordingLogger{}
	f := reddit.NewFetcher([]reddit.FeedConfig{{Subreddit: "examples"}}, cfg, logger)
	f.BaseURL = "http://169.254.169.254"
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !strings.Contains(strings.Join(logger.lines, "\n"), "destination not allowed") {
		t.Fatalf("want a disallowed-destination warning, got %v", logger.lines)
	}
}
