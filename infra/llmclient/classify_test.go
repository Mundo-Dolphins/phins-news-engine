package llmclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestClient_Classify_ParsesValidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"role":    "assistant",
					"content": `{"labels": [{"article_id": "a1", "label": "publish", "confidence": 0.92, "reason": "primary source, on-topic"}]}`,
				}},
			},
		})
	}))
	defer server.Close()

	c := New(Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	result, err := c.Classify(context.Background(), "POLICY TEXT", []appout.EditorialTeacherArticle{
		{ArticleID: "a1", Title: "Isotopes sign new WR", Summary: "..."},
	})
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if len(result.Labels) != 1 {
		t.Fatalf("Labels = %v, want 1 entry", result.Labels)
	}
	got := result.Labels[0]
	if got.ArticleID != "a1" || got.Label != "publish" || got.Confidence != 0.92 || got.Reason == "" {
		t.Errorf("Labels[0] = %+v, unexpected", got)
	}
}

func TestClient_Classify_StripsCodeFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"role":    "assistant",
					"content": "```json\n{\"labels\": [{\"article_id\": \"a1\", \"label\": \"discard\", \"confidence\": 0.4, \"reason\": \"off-topic\"}]}\n```",
				}},
			},
		})
	}))
	defer server.Close()

	c := New(Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	result, err := c.Classify(context.Background(), "POLICY TEXT", []appout.EditorialTeacherArticle{{ArticleID: "a1"}})
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if len(result.Labels) != 1 || result.Labels[0].Label != "discard" {
		t.Fatalf("Labels = %v, want one discard label", result.Labels)
	}
}

func TestClient_Classify_InvalidJSONReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "not json"}},
			},
		})
	}))
	defer server.Close()

	c := New(Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	_, err := c.Classify(context.Background(), "POLICY TEXT", []appout.EditorialTeacherArticle{{ArticleID: "a1"}})
	if err == nil {
		t.Fatal("Classify() error = nil, want error for invalid JSON")
	}
}
