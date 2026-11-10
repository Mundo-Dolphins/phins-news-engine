package events

import (
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SetDiscoveryLLM substitutes the discovery LLM builder and returns the
// restore function.
func SetDiscoveryLLM(llm appout.EventRuleDiscoveryLLM) func() {
	old := newDiscoveryLLM
	newDiscoveryLLM = func(config.OfflineTrainingLLMConfig) appout.EventRuleDiscoveryLLM { return llm }
	return func() { newDiscoveryLLM = old }
}

// SetGateDataset substitutes the gate dataset path and returns the restore
// function.
func SetGateDataset(path string) func() {
	old := gateDatasetPath
	gateDatasetPath = path
	return func() { gateDatasetPath = old }
}

// SetAfterRejectFailingListed installs a hook that runs between listing and
// rejecting in events.rules_reject_failing and returns the restore function.
func SetAfterRejectFailingListed(f func()) func() {
	afterRejectFailingListed = f
	return func() { afterRejectFailingListed = nil }
}
