package cycle

import (
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SetDiscoveryLLM replaces the entity step's discovery LLM factory until the
// returned restore is called.
func SetDiscoveryLLM(f entitywiring.DiscoveryLLMFactory) (restore func()) {
	old := discoveryLLM
	discoveryLLM = f
	return func() { discoveryLLM = old }
}

// SetEventRuleLLM replaces the event rules discovery LLM until the returned
// restore is called.
func SetEventRuleLLM(llm out.EventRuleDiscoveryLLM) (restore func()) {
	old := eventRuleLLM
	eventRuleLLM = llm
	return func() { eventRuleLLM = old }
}

// SetEventRulesDataset replaces the gate dataset until the returned restore
// is called.
func SetEventRulesDataset(path string) (restore func()) {
	old := eventRulesDataset
	eventRulesDataset = path
	return func() { eventRulesDataset = old }
}
