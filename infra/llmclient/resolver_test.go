// infra/llmclient/resolver_test.go
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

func TestResolver_Resolve_ParsesResolvedResult(t *testing.T) {
	content := `{"resolved":true,"type":"player","canonical_name":"Random Newcomer","confidence":0.85}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionResponse(t, content))
	}))
	defer server.Close()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	result, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{
		Text: "Newcomer", NormalizedText: "newcomer", Field: "title", Context: "Isotopes sign Newcomer",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !result.Resolved || result.Type != "player" || result.CanonicalName != "Random Newcomer" || result.Confidence != 0.85 {
		t.Fatalf("Resolve() = %+v, want resolved player Random Newcomer 0.85", result)
	}
}

func TestResolver_Resolve_ParsesUnresolvedResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, `{"resolved":false}`))
	}))
	defer server.Close()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	result, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "Three Things", NormalizedText: "three things", Field: "title", Context: "Three Things To Know"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if result.Resolved {
		t.Fatalf("Resolve() = %+v, want Resolved=false", result)
	}
}

func TestResolver_Resolve_StripsMarkdownFences(t *testing.T) {
	fenced := "```json\n{\"resolved\":true,\"type\":\"coach\",\"canonical_name\":\"Jeff Harrowby\",\"confidence\":0.8}\n```"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, fenced))
	}))
	defer server.Close()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	result, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "Coach Harrowby", NormalizedText: "coach harrowby", Field: "body", Context: "Coach Harrowby spoke today"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !result.Resolved || result.CanonicalName != "Jeff Harrowby" {
		t.Fatalf("Resolve() = %+v, want resolved Jeff Harrowby", result)
	}
}

func TestResolver_Resolve_InvalidJSONReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, "not json"))
	}))
	defer server.Close()

	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	if _, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "X Y", NormalizedText: "x y"}); err == nil {
		t.Fatal("Resolve() error = nil, want error for invalid JSON")
	}
}
