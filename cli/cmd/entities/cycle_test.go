package entities

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestValidateCycleVerification(t *testing.T) {
	for _, ok := range []string{"local", "full", "none"} {
		if err := ValidateCycleVerification(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if err := ValidateCycleVerification("web"); err == nil {
		t.Error("an unknown mode must be rejected")
	}
}

func TestCycleStepDisabledReason(t *testing.T) {
	var cfg config.Config
	if CycleStepDisabledReason(&cfg) == "" {
		t.Fatal("a configuration without entity_discovery must report why the step is off")
	}
	cfg.EntityDiscovery.Enabled = true
	cfg.Clustering.EntityCatalog.ManualPath = "m.yml"
	cfg.Clustering.EntityCatalog.GeneratedPath = "g.yml"
	cfg.Dictionary.Path = "d.yml"
	if r := CycleStepDisabledReason(&cfg); r != "" {
		t.Fatalf("a fully configured step must run, got %q", r)
	}
	cfg.Dictionary.Path = ""
	if CycleStepDisabledReason(&cfg) == "" {
		t.Fatal("a missing dictionary path must disable the step")
	}
}
