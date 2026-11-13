package cycle

import "github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"

// SetDiscoveryLLM replaces the entity step's discovery LLM factory until the
// returned restore is called.
func SetDiscoveryLLM(f entitywiring.DiscoveryLLMFactory) (restore func()) {
	old := discoveryLLM
	discoveryLLM = f
	return func() { discoveryLLM = old }
}
