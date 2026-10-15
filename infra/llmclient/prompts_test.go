package llmclient_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestDefaultPrompts_AreTeamNeutral(t *testing.T) {
	d := llmclient.DefaultPrompts()
	for name, text := range map[string]string{
		"EntityDiscovery": d.EntityDiscovery, "EntityResolution": d.EntityResolution,
		"EntityVerification": d.EntityVerification, "EventTopics": d.EventTopics, "EventPhrases": d.EventPhrases,
	} {
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s default prompt is empty", name)
		}
		if !strings.Contains(text, "NFL") {
			t.Errorf("%s default prompt lost its NFL scope", name)
		}
	}
	if got := strings.Count(d.EventPhrases, "%s"); got != 2 {
		t.Errorf("EventPhrases has %d %%s verbs, want 2", got)
	}
}

func TestSetPrompts_OverridesOnlyNonEmptyFields(t *testing.T) {
	t.Cleanup(func() { llmclient.SetPrompts(llmclient.Prompts{}) })
	llmclient.SetPrompts(llmclient.Prompts{EntityResolution: "host resolution prompt"})
	got := llmclient.CurrentPrompts()
	if got.EntityResolution != "host resolution prompt" {
		t.Errorf("EntityResolution = %q, want the override", got.EntityResolution)
	}
	if got.EntityDiscovery != llmclient.DefaultPrompts().EntityDiscovery {
		t.Errorf("EntityDiscovery changed although its override was empty")
	}

	var system string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		_, _ = w.Write(chatCompletionResponse(t, `{"resolved":false}`))
	}))
	defer server.Close()
	resolver := llmclient.NewResolver(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	if _, err := resolver.Resolve(context.Background(), appout.EntityResolutionCandidate{Text: "X", NormalizedText: "x", Field: "title", Context: "X"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.HasPrefix(system, "host resolution prompt") {
		t.Errorf("system prompt sent = %q, want the host override", system)
	}

	llmclient.SetPrompts(llmclient.Prompts{})
	if llmclient.CurrentPrompts() != llmclient.DefaultPrompts() {
		t.Errorf("an empty Prompts must restore every default")
	}
}
