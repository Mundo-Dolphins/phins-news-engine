package train_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

func TestTrainMine_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "train", "mine")
}

func TestTrainRun_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "train", "run")
}

// promotedModelConfig returns a fixture config whose model.path holds a
// promoted model (made by a plain `train run`) and which has one unreviewed
// article left for the LLM to look at, with server-a pointing at a fake.
func promotedModelConfig(t *testing.T, fake *testsupport.FakeLLMServer) string {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	seedGoldLabels(t, filepath.Join(dir, "operational.db"), 15)
	testsupport.SetOfflineLLMTimeout(t, configPath)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "offline_training:\n  llm:\n"
	if !strings.Contains(string(raw), anchor) {
		t.Fatal("fixture no longer has the offline_training.llm block this helper patches")
	}
	if err := os.WriteFile(configPath, []byte(strings.Replace(string(raw), anchor, "offline_training:\n  mining:\n    batch_limit: 10\n  llm:\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	withModel := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, withModel, filepath.Join(dir, "models", "editorial", "current"))
	if out, err := testsupport.Execute(t, "--config", withModel, "train", "run",
		"--dataset", filepath.Join(t.TempDir(), "dataset.yml"), "--candidates", filepath.Join(t.TempDir(), "candidates")); err != nil {
		t.Fatalf("seed promoted model: %v\n%s", err, out)
	}
	testsupport.SeedUnreviewedArticle(t, withModel, "unreviewed")
	fake.AppendServerA(t, withModel)
	return withModel
}

func TestTrainMine_LLMServerReachesTheClassifier(t *testing.T) {
	fake := testsupport.NewFakeLLMServer(t, "m1")
	configPath := promotedModelConfig(t, fake)

	out, err := testsupport.Execute(t, "--config", configPath, "train", "mine", "--llm-server", "server-a", "--llm-model", "m1")
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	fake.RequireModel(t, "m1")
}

func TestTrainRun_LLMServerReachesTheClassifier(t *testing.T) {
	fake := testsupport.NewFakeLLMServer(t, "m1")
	configPath := promotedModelConfig(t, fake)

	out, err := testsupport.Execute(t, "--config", configPath, "train", "run", "--force",
		"--dataset", filepath.Join(t.TempDir(), "dataset.yml"), "--candidates", filepath.Join(t.TempDir(), "candidates"),
		"--llm-server", "server-a", "--llm-model", "m1")
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	fake.RequireModel(t, "m1")
}
