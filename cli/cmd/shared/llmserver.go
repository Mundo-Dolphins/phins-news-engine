package shared

import "github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"

// ListLLMModels lists the models an LLM server offers. It is a variable so
// tests can stub the live network call.
var ListLLMModels llmservers.ListFunc = llmservers.ListModels
