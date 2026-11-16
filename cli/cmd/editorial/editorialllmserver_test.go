package editorial_test

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	toolsevents "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/events"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestEditorialClassify_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "editorial", "classify")
}

func TestEditorialClassifyEval_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "editorial", "classify-eval")
}

func TestEditorialCycle_RejectsUnlistedLLMModel(t *testing.T) {
	configPath, _ := eventRulesFixture(t)
	testsupport.ExpectUnlistedLLMModel(t, configPath, append([]string{"editorial", "cycle"}, baseCycleArgs...)...)
}

func TestEditorialCycle_LLMServerReachesTheEventRuleLLM(t *testing.T) {
	configPath, _ := eventRulesFixture(t)
	testsupport.AppendLLMServers(t, configPath)
	testsupport.StubLLMModels(t, "m1")
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedTitled(t, dbPath, "g1", "Veteran puts pen to paper in Springfield")
	markGeneric(t, dbPath, "g1")

	var gotURL, gotModel string
	old := toolsevents.NewCycleLLM
	toolsevents.NewCycleLLM = func(_ *cobra.Command, c config.OfflineTrainingLLMConfig) appout.EventRuleDiscoveryLLM {
		gotURL, gotModel = c.BaseURL, c.Model
		return &fakeEventRuleLLM{}
	}
	t.Cleanup(func() { toolsevents.NewCycleLLM = old })

	out, err := testsupport.ExecuteWithStdin(t, "p\na\n", eventRuleArgs(t, configPath, "--llm-server", "server-a", "--llm-model", "m1")...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if gotURL != testsupport.LLMServerAURL || gotModel != "m1" {
		t.Fatalf("event rule LLM built with %q %q, want %q m1\n%s", gotURL, gotModel, testsupport.LLMServerAURL, out)
	}
}
