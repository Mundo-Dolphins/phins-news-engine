package youtube_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/youtube"
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
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	logger := &recordingLogger{}
	f := youtube.NewFetcher(server.URL+"/?token="+secretToken, []youtube.AccountConfig{{Name: "C", ChannelID: "UCexample"}}, testClientConfig(), logger)
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	logged := strings.Join(logger.lines, "\n")
	if logged == "" || strings.Contains(logged, secretToken) {
		t.Fatalf("log must record the failure without the token:\n%s", logged)
	}
}
