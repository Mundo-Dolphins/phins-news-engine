// cli/cmd/events/cycle_test.go
package events

import (
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// repoPath resolves a repository-relative path from this package directory.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	return repofs.Path(t, filepath.FromSlash(rel))
}

func enabledConfig(t *testing.T) *config.Config {
	t.Helper()
	var cfg config.Config
	cfg.OfflineTraining.LLM.BaseURL = "http://127.0.0.1:1/v1"
	cfg.OfflineTraining.LLM.Model = "test"
	cfg.Clustering.RulesPath = repoPath(t, "configs/example/event_clustering_rules.yml")
	cfg.Clustering.EntityCatalog.ManualPath = repoPath(t, "configs/example/entity_catalog_manual.yml")
	cfg.Clustering.EntityCatalog.GeneratedPath = repoPath(t, "configs/example/entity_catalog_generated.yml")
	return &cfg
}

func TestCycleStepDisabledReason(t *testing.T) {
	cfg := enabledConfig(t)
	if r := CycleStepDisabledReason(cfg); r != "" {
		t.Fatalf("a fully configured step must run, got %q", r)
	}
	for name, mutate := range map[string]func(*config.Config){
		"no llm base_url":  func(c *config.Config) { c.OfflineTraining.LLM.BaseURL = "" },
		"no llm model":     func(c *config.Config) { c.OfflineTraining.LLM.Model = "" },
		"no rules path":    func(c *config.Config) { c.Clustering.RulesPath = "" },
		"no entity paths":  func(c *config.Config) { c.Clustering.EntityCatalog.ManualPath = "" },
		"no generated cat": func(c *config.Config) { c.Clustering.EntityCatalog.GeneratedPath = "" },
	} {
		c := enabledConfig(t)
		mutate(c)
		if CycleStepDisabledReason(c) == "" {
			t.Errorf("%s: the step must report why it is off", name)
		}
	}
}
