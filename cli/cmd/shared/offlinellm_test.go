package shared_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestOfflineTrainingLLMClientConfig_CarriesMaxTokensAndThinking(t *testing.T) {
	got := shared.OfflineTrainingLLMClientConfig(config.OfflineTrainingLLMConfig{
		BaseURL: "http://x/v1", Model: "m", TimeoutSeconds: 90, MaxTokens: 2048, DisableThinking: true,
	})
	if got.MaxTokens != 2048 || got.Timeout.Seconds() != 90 || got.Model != "m" {
		t.Errorf("config = %+v, want MaxTokens 2048, 90s timeout, model m", got)
	}
	if got.EnableThinking == nil || *got.EnableThinking || got.ReasoningEffort != "none" {
		t.Errorf("thinking not disabled: %+v", got)
	}

	plain := shared.OfflineTrainingLLMClientConfig(config.OfflineTrainingLLMConfig{Model: "m"})
	if plain.EnableThinking != nil || plain.ReasoningEffort != "" {
		t.Errorf("thinking controls set without disable_thinking: %+v", plain)
	}
}
