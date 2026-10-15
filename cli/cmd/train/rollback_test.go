package train_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

func TestRollbackModelCmd_RestoresPreviousVersion(t *testing.T) {
	configPath := testsupport.WriteFixture(t) // model.path is "" in the base fixture; override below
	dir := filepath.Dir(configPath)
	promotedPath := filepath.Join(dir, "models", "editorial", "current")

	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, promotedPath)

	firstCandidate := testsupport.WriteGatePassingCandidateModel(t)
	if _, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "promote", firstCandidate, "--version", "v1.0.0"); err != nil {
		t.Fatalf("promote v1.0.0: %v", err)
	}
	secondCandidate := testsupport.WriteGatePassingCandidateModel(t)
	if _, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "promote", secondCandidate, "--version", "v2.0.0"); err != nil {
		t.Fatalf("promote v2.0.0: %v", err)
	}

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "rollback")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "rolled back") {
		t.Errorf("output = %q, want it to confirm the rollback", out)
	}

	m, err := editorialmodel.Load(promotedPath)
	if err != nil {
		t.Fatalf("editorialmodel.Load(%s) error = %v", promotedPath, err)
	}
	if m.Version != "v1.0.0" {
		t.Errorf("current model Version after rollback = %q, want %q (the first-promoted version)", m.Version, "v1.0.0")
	}
}

func TestRollbackModelCmd_NoPreviousSlotFails(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	promotedPath := filepath.Join(dir, "models", "editorial", "current")
	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, promotedPath)

	candidateDir := testsupport.WriteGatePassingCandidateModel(t)
	if _, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "promote", candidateDir, "--version", "v1.0.0"); err != nil {
		t.Fatalf("promote v1.0.0: %v", err)
	}

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "rollback")
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when no previous/ slot exists yet")
	}
	if strings.Contains(out, "rolled back") {
		t.Errorf("output = %q, want no success claim when rollback failed", out)
	}
}
