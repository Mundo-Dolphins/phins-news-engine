package rss_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/rss"
)

const secretToken = "SECRET-TOKEN-123"

// recordingLogger keeps every Warn/Error message and its values as text.
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

func (l *recordingLogger) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestFetch_LoggedErrorsNeverContainToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	scraper := &fakeScraper{err: errors.New("scraper down")}
	logger := &recordingLogger{}
	feedURL := server.URL + "/feed?token=" + secretToken
	f := rss.NewFetcherWithScraper([]rss.FeedConfig{{Name: "Secret", URL: feedURL}}, testClientConfig(), scraper, logger)
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	logged := logger.all()
	if logged == "" {
		t.Fatal("expected the failure to be logged")
	}
	if strings.Contains(logged, secretToken) {
		t.Fatalf("log leaks the token:\n%s", logged)
	}
}

func TestFetch_DisallowedFeedURLSkipsScraperFallback(t *testing.T) {
	scraper := &fakeScraper{}
	cfg := testClientConfig()
	cfg.AllowPrivateHosts = false
	f := rss.NewFetcherWithScraper([]rss.FeedConfig{{Name: "Metadata", URL: "http://169.254.169.254/latest/meta-data/"}}, cfg, scraper, noopLogger{})
	articles, err := f.Fetch(context.Background())
	if err != nil || len(articles) != 0 {
		t.Fatalf("Fetch() = %v, %v; want no articles and nil error", articles, err)
	}
	if scraper.calls != 0 {
		t.Fatalf("scraper called %d times for a disallowed URL", scraper.calls)
	}
}
