package editorial_test

import (
	"os"
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

func TestEditorialClassify_LLMServerReachesTheClassifier(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	testsupport.SeedUnreviewedArticle(t, configPath, "a1")
	testsupport.SetOfflineLLMTimeout(t, configPath)
	fake := testsupport.NewFakeLLMServer(t, "m1")
	fake.AppendServerA(t, configPath)

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "classify", "--all", "--llm-server", "server-a", "--llm-model", "m1")
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	fake.RequireModel(t, "m1")
}

func TestEditorialClassifyEval_LLMServerReachesTheClassifier(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	testsupport.SetOfflineLLMTimeout(t, configPath)
	fake := testsupport.NewFakeLLMServer(t, "m1")
	fake.AppendServerA(t, configPath)
	dataset := filepath.Join(t.TempDir(), "dataset.yml")
	const examples = "examples:\n  - article_id: e1\n    title: Isotopes win big game\n    source_type: rss\n    domain: example.com\n    label: publish\n"
	if err := os.WriteFile(dataset, []byte(examples), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := testsupport.Execute(t, "--config", configPath, "editorial", "classify-eval", "--dataset", dataset, "--fold", "all", "--llm-server", "server-a", "--llm-model", "m1")
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	fake.RequireModel(t, "m1")
}
