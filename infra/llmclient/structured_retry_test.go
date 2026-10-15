// infra/llmclient/structured_retry_test.go
package llmclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type scriptedCompletion struct {
	content      string
	finishReason string
}

// scriptedServer replies with each completion in order (repeating the last one
// if over-called) and records the max_tokens of every request it receives.
type scriptedServer struct {
	*httptest.Server
	mu        sync.Mutex
	requests  int
	maxTokens []int
}

func newScriptedServer(t *testing.T, script ...scriptedCompletion) *scriptedServer {
	t.Helper()
	s := &scriptedServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		idx := s.requests
		s.requests++
		s.maxTokens = append(s.maxTokens, body.MaxTokens)
		s.mu.Unlock()
		if idx >= len(script) {
			idx = len(script) - 1
		}
		resp := map[string]any{"choices": []map[string]any{{
			"message":       map[string]any{"content": script[idx].content},
			"finish_reason": script[idx].finishReason,
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.Close)
	return s
}

const validEntitiesJSON = `{"entities":[{"name":"Jeff Harrowby","type":"coach","confidence":0.8}]}`

var discoverArticles = []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}}

func newDiscoverClient(url string, maxTokens int) *llmclient.Client {
	return llmclient.New(llmclient.Config{BaseURL: url, Model: "m", MaxTokens: maxTokens, Timeout: 5 * time.Second})
}

func TestClient_Discover_ValidFirstResponseMakesOneRequest(t *testing.T) {
	s := newScriptedServer(t, scriptedCompletion{validEntitiesJSON, "stop"})
	res, err := newDiscoverClient(s.URL, 1024).Discover(context.Background(), discoverArticles)
	if err != nil || len(res.Entities) != 1 {
		t.Fatalf("Discover() = %+v, %v; want one entity", res, err)
	}
	if s.requests != 1 {
		t.Fatalf("requests = %d, want 1", s.requests)
	}
}

func TestClient_Discover_TruncatedThenValidRecoversAndRaisesMaxTokens(t *testing.T) {
	s := newScriptedServer(t,
		scriptedCompletion{`{"entities":[{"name":"Jeff Ha`, "length"},
		scriptedCompletion{validEntitiesJSON, "stop"})
	res, err := newDiscoverClient(s.URL, 1024).Discover(context.Background(), discoverArticles)
	if err != nil || len(res.Entities) != 1 || res.Entities[0].Name != "Jeff Harrowby" {
		t.Fatalf("Discover() = %+v, %v; want recovered entity", res, err)
	}
	if s.requests != 2 {
		t.Fatalf("requests = %d, want 2", s.requests)
	}
	if got := s.maxTokens; got[0] != 1024 || got[1] != 2048 {
		t.Fatalf("max_tokens per request = %v, want [1024 2048]", got)
	}
}

func TestClient_Discover_EmptyThenValidRecoversWithoutRaisingMaxTokens(t *testing.T) {
	s := newScriptedServer(t,
		scriptedCompletion{"", "stop"},
		scriptedCompletion{validEntitiesJSON, "stop"})
	res, err := newDiscoverClient(s.URL, 1024).Discover(context.Background(), discoverArticles)
	if err != nil || len(res.Entities) != 1 {
		t.Fatalf("Discover() = %+v, %v; want recovered entity", res, err)
	}
	if got := s.maxTokens; len(got) != 2 || got[1] != 1024 {
		t.Fatalf("max_tokens per request = %v, want [1024 1024]", got)
	}
}

func TestClient_Discover_UnsetMaxTokensStaysUnsetOnRetry(t *testing.T) {
	s := newScriptedServer(t,
		scriptedCompletion{`{"entities":[`, "length"},
		scriptedCompletion{validEntitiesJSON, "stop"})
	if _, err := newDiscoverClient(s.URL, 0).Discover(context.Background(), discoverArticles); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := s.maxTokens; got[1] != 0 {
		t.Fatalf("retry max_tokens = %d, want 0 (provider default) when none configured", got[1])
	}
}

func TestClient_Discover_TwoTruncatedResponsesFailAfterExactlyTwoAttempts(t *testing.T) {
	s := newScriptedServer(t, scriptedCompletion{`{"entities":[{"name":"Jeff Ha`, "length"})
	res, err := newDiscoverClient(s.URL, 1024).Discover(context.Background(), discoverArticles)
	if err == nil {
		t.Fatalf("Discover() = %+v, want error", res)
	}
	if len(res.Entities) != 0 {
		t.Fatalf("Discover() entities = %+v, want none (no partial repair)", res.Entities)
	}
	if s.requests != 2 {
		t.Fatalf("requests = %d, want exactly 2", s.requests)
	}
	msg := err.Error()
	for _, want := range []string{
		"structured response invalid after 2 attempts",
		"finish_reason=length",
		"content_bytes=29",
		"max_tokens=2048",
		`parse_error="invalid JSON: unexpected end of JSON input"`,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "Jeff Ha") {
		t.Fatalf("error %q leaks response content", msg)
	}
}

func TestClient_Discover_TwoEmptyResponsesReportContentBytesZero(t *testing.T) {
	s := newScriptedServer(t, scriptedCompletion{"", "stop"})
	_, err := newDiscoverClient(s.URL, 1024).Discover(context.Background(), discoverArticles)
	if err == nil || !strings.Contains(err.Error(), "content_bytes=0") || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("Discover() error = %v, want empty-response diagnostics with content_bytes=0", err)
	}
	if s.requests != 2 {
		t.Fatalf("requests = %d, want 2", s.requests)
	}
}

func TestResolver_Resolve_InvalidJSONDoesNotRetry(t *testing.T) {
	s := newScriptedServer(t, scriptedCompletion{`{"resolved":`, "length"})
	r := llmclient.NewResolver(llmclient.Config{BaseURL: s.URL, Model: "m", MaxTokens: 256, Timeout: 5 * time.Second})
	_, _ = r.Resolve(context.Background(), appout.EntityResolutionCandidate{})
	if s.requests != 1 {
		t.Fatalf("resolver requests = %d, want 1 (no structured-output retry online)", s.requests)
	}
}
