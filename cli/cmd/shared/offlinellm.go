package shared

import (
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
)

// OfflineTrainingLLMClientConfig builds the LLM teacher client config
// shared by `editorial classify`, `train mine` and `train run` from
// offline_training.llm.
func OfflineTrainingLLMClientConfig(c config.OfflineTrainingLLMConfig) llmclient.Config {
	out := llmclient.Config{
		BaseURL: c.BaseURL, APIKey: c.APIKey,
		Model: c.Model, Temperature: c.Temperature,
		MaxTokens: c.MaxTokens, Timeout: c.Timeout(),
	}
	if c.DisableThinking {
		out = out.WithThinkingDisabled()
	}
	return out
}
