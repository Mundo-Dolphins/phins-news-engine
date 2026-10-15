// infra/llmclient/llmclient_test.go
package llmclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func chatCompletionResponse(t *testing.T, content string) []byte {
	t.Helper()
	body := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal fake response: %v", err)
	}
	return b
}

func TestClient_Discover_ParsesEntitiesFromContent(t *testing.T) {
	entitiesJSON := `{"entities":[{"name":"Jai'Rene' Mossbank","type":"player","aliases":["Mossbank"],"metadata":{"position":"CB"},"confidence":0.9,"source_article_ids":["a1"]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatCompletionResponse(t, entitiesJSON))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	result, err := c.Discover(context.Background(), []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Entities) != 1 || result.Entities[0].Name != "Jai'Rene' Mossbank" || result.Entities[0].Metadata["position"] != "CB" {
		t.Fatalf("Discover() = %+v, want one parsed entity", result)
	}
	if got := result.Entities[0].SourceArticleIDs; len(got) != 1 || got[0] != "a1" {
		t.Fatalf("Discover() SourceArticleIDs = %+v, want [\"a1\"] (json:\"source_article_ids\" must round-trip)", got)
	}
}

func TestClient_Discover_SendsOptionalThinkControl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got, ok := body["reasoning_effort"].(string); !ok || got != "none" {
			t.Fatalf("request reasoning_effort = %#v, want none", body["reasoning_effort"])
		}
		if got, ok := body["enable_thinking"].(bool); !ok || got {
			t.Fatalf("request enable_thinking = %#v, want false", body["enable_thinking"])
		}
		kwargs, ok := body["chat_template_kwargs"].(map[string]any)
		if !ok || kwargs["enable_thinking"] != false || kwargs["preserve_thinking"] != false {
			t.Fatalf("request chat_template_kwargs = %#v, want thinking disabled", body["chat_template_kwargs"])
		}
		_, _ = w.Write(chatCompletionResponse(t, `{"entities":[]}`))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second}.WithThinkingDisabled())
	if _, err := c.Discover(context.Background(), []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}}); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
}

func TestClient_Discover_StripsMarkdownFences(t *testing.T) {
	fenced := "```json\n{\"entities\":[{\"name\":\"Jeff Harrowby\",\"type\":\"coach\",\"confidence\":0.8}]}\n```"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, fenced))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	result, err := c.Discover(context.Background(), []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Entities) != 1 || result.Entities[0].Name != "Jeff Harrowby" {
		t.Fatalf("Discover() = %+v, want the fenced JSON parsed", result)
	}
}

func TestClient_Discover_RetriesWithoutResponseFormatOn4xx(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, hasFormat := body["response_format"]; hasFormat {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"response_format not supported"}`))
			return
		}
		_, _ = w.Write(chatCompletionResponse(t, `{"entities":[]}`))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	_, err := c.Discover(context.Background(), []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}})
	if err != nil {
		t.Fatalf("Discover() error = %v, want the retry-without-response_format to succeed", err)
	}
	if attempts != 2 {
		t.Fatalf("server got %d requests, want 2 (first with response_format, retry without)", attempts)
	}
}

func TestClient_Discover_ReturnsErrorOnUnreachableServer(t *testing.T) {
	c := llmclient.New(llmclient.Config{BaseURL: "http://127.0.0.1:1", Model: "test-model", Timeout: 1 * time.Second})
	_, err := c.Discover(context.Background(), []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}})
	if err == nil {
		t.Fatal("Discover() error = nil, want an error for an unreachable server")
	}
}

func TestClient_Discover_ReturnsErrorOnInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, "not json at all"))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	_, err := c.Discover(context.Background(), []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "t", Summary: "s"}})
	if err == nil || !strings.Contains(err.Error(), "llmclient") {
		t.Fatalf("Discover() error = %v, want a wrapped llmclient error for invalid JSON content", err)
	}
}
