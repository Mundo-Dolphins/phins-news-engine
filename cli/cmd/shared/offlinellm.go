package shared

import (
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

// OfflineTrainingLLMClientConfig builds the LLM teacher client config
// shared by `editorial classify`, `train mine` and `train run` from
// offline_training.llm. It delegates to trainwiring.LLMClientConfig.
func OfflineTrainingLLMClientConfig(c config.OfflineTrainingLLMConfig) llmclient.Config {
	return trainwiring.LLMClientConfig(c)
}
