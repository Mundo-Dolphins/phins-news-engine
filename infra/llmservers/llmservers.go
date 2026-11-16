// Package llmservers resolves, lists and applies the operator-configured
// OpenAI-compatible LLM servers that the CLI and the admin can select.
package llmservers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
)

const listTimeout = 5 * time.Second

// Selection is a chosen server name and model.
type Selection struct{ Server, Model string }

// ListFunc lists the model ids a server offers.
type ListFunc func(ctx context.Context, s config.LLMServerConfig) ([]string, error)

// Find returns the configured server called name.
func Find(servers []config.LLMServerConfig, name string) (config.LLMServerConfig, error) {
	if len(servers) == 0 {
		return config.LLMServerConfig{}, fmt.Errorf("no llm_servers configured")
	}
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		if s.Name == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	return config.LLMServerConfig{}, fmt.Errorf("unknown llm server %q (configured: %s)", name, strings.Join(names, ", "))
}

// ListModels queries GET {base_url}/models and returns the sorted model ids.
func ListModels(ctx context.Context, s config.LLMServerConfig) ([]string, error) {
	key := ""
	if s.APIKeyEnv != "" {
		key = os.Getenv(s.APIKeyEnv)
	}
	if _, err := netguard.ValidateURL(s.BaseURL, netguard.AllowPrivate); err != nil {
		return nil, fmt.Errorf("base_url: %w", netguard.RedactError(err, key))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", netguard.RedactError(err, key))
	}
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := netguard.NewClient(listTimeout, listTimeout, netguard.AllowPrivate)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", netguard.RedactError(err, key))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list models: unexpected status %d", resp.StatusCode)
	}
	data, err := netguard.ReadBody(resp.Body, netguard.JSONBodyLimit)
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// Check verifies model is offered by server s.
func Check(ctx context.Context, list ListFunc, s config.LLMServerConfig, model string) error {
	models, err := list(ctx, s)
	if err != nil {
		return fmt.Errorf("llm server %s unavailable: %w", s.Name, err)
	}
	if !slices.Contains(models, model) {
		return fmt.Errorf("model %q is not available on %s (run: llm models --llm-server %s)", model, s.Name, s.Name)
	}
	return nil
}

// Apply returns a copy of cfg whose two LLM blocks use server s and model.
// Only base_url, model and the API key change.
func Apply(cfg *config.Config, s config.LLMServerConfig, model string) *config.Config {
	out := *cfg
	key := ""
	if s.APIKeyEnv != "" {
		key = os.Getenv(s.APIKeyEnv)
	}
	out.OfflineTraining.LLM.BaseURL, out.OfflineTraining.LLM.Model, out.OfflineTraining.LLM.APIKey = s.BaseURL, model, key
	out.EntityDiscovery.LLM.BaseURL, out.EntityDiscovery.LLM.Model, out.EntityDiscovery.LLM.APIKey = s.BaseURL, model, key
	return &out
}
